# Integrations: call events and webhooks

Hello publishes what happens to every call as small, ordered **call events**.
An integration reads them from a live stream or from the stored history and
uses them to show who is calling, log calls in another system or trigger an
automation. This page covers the first part of the integrations feature (spec
`.procoder/specs/integrations.md`, phases P1 and P2). Click-to-call,
recording links and the CRM connectors build on the same events and arrive in
later phases. [Webhooks](#webhooks) push the events to your server.

## What is emitted

| Event                  | When                                                                                                                                   |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `call.ringing`         | An extension starts ringing. A call that rings several extensions (a ring group) yields one event per ringing extension.               |
| `call.answered`        | An extension answered. The calling extension of an internal or outbound call gets one too.                                             |
| `call.ended`           | The call ended, for each extension it involved.                                                                                        |
| `call.missed`          | The call reached an extension and nobody answered. A call answered by another member of a ring group is not missed for the others.     |
| `call.recording.ready` | A recording of the call was stored. It is stored after the call ends, so it follows `call.ended`. Carries `recordingId`, `durationMs`. |
| `call.completed`       | The call detail record was stored. Carries `cdrId`; this is the point to log the call elsewhere.                                       |

Pages, intercoms and conference legs do not produce call events.

Every event is about one extension (`data.extension`, the subject). A call
between two extensions therefore produces an event for each of them, with
different ids. Events for the same call share `data.callId`, which is also the
CDR's correlation id.

```json
{
  "apiVersion": "1",
  "id": "evt_3f9c1b7a52d84e0a9b6c0d1e7f2a4c58",
  "type": "call.missed",
  "createdAt": "2026-10-26T09:14:03.512Z",
  "data": {
    "callId": "5b0f6e9c2a1d4f7e",
    "direction": "inbound",
    "extension": "201",
    "userId": 7,
    "sequence": 4,
    "from": { "number": "+3211223344", "name": "ACME NV" },
    "to": { "number": "201", "name": "Support", "extension": "201" },
    "trunk": "carrier-1",
    "startedAt": "2026-10-26T09:13:33.100Z",
    "endedAt": "2026-10-26T09:14:03.500Z",
    "durationMs": 30400,
    "disposition": "no_answer",
    "reason": "no_answer"
  }
}
```

`userId` is the user the subject extension is linked to, resolved when the
event is stored (absent when no user owns it). `sequence` orders the events of
one call (`callId`): it only increases, across all the call's extensions and
across a takeover by another node, but is not contiguous. Use it, not arrival
order, to order a call's events.

A call answered by a queue agent or through an IVR menu produces the same
events for the agent or extension as a direct call (`call.ringing` while it
rings, `call.answered`, `call.ended`), and an agent who rang while the caller
gave up gets `call.missed`. A call taken over by another node after a failure
keeps its answering extension, so it is not reported missed.

`reason` on `call.missed` is `no_answer`, `busy`, `dnd`, `cancelled` (the
caller hung up while it rang) or `unreachable` (no registered phone).
`disposition` on `call.ended` and `call.completed` is `answered`, `no_answer`,
`busy`, `cancelled`, `unavailable` or `failed`. Fields that do not apply to an
event type are left out. Audio, SIP bodies and DTMF digits are never in an
event. The envelope version is `apiVersion`; later versions only add fields.

## Delivery guarantees

- **At least once.** An event can arrive twice (a reconnect, a takeover of a
  call by another node). `id` is stable and unique: **dedupe on it.**
- **Ordered by cursor.** Each event has a `cursor`, its position in one global
  order. Events of one call are in order; events of different calls are
  ordered by when Hello stored them.
- **Best effort on the SIP side.** The SIP path never waits for events: they go
  into a bounded in-memory queue (1,000) and then to Valkey. If Valkey is down
  or the queue is full the event is dropped and counted in
  `hello_events_dropped_total`; calls are unaffected.
- **Kept for 7 days** (`HELLO_EVENT_RETENTION_DAYS`, 1 to 30).

## The live stream

`GET /api/v1/events/stream` is a server-sent event stream (`text/event-stream`).

```sh
curl -N -H "Authorization: Bearer $TOKEN" -H "Accept: text/event-stream" \
  "https://hello.example/api/v1/events/stream?types=call.ringing,call.missed"
```

```text
retry: 3000
: stream open

id: 1761470043512-0
event: call.ringing
data: {"apiVersion":"1","id":"evt_…","type":"call.ringing","createdAt":"…","data":{…}}

: keepalive
```

- `id:` is the event's cursor, `event:` its type and `data:` the event as JSON.
- A comment (`: keepalive`) arrives every 15 seconds on an idle stream. A client
  that has seen nothing for about 45 seconds should reconnect.
- **Filters** (query parameters): `types` is a comma-separated list of exact
  types or a prefix such as `call.*`; `extension` keeps one extension.
- **Resume.** After any disconnect, reconnect with the `Last-Event-ID` header
  (browsers' `EventSource` does this by itself) or the `lastEventId` query
  parameter. Hello replays everything after that cursor, from the stored
  history and the stream, then continues live, without gaps or repeats. A
  stream that was opened without `Last-Event-ID` starts at "now". A replay is
  capped at 10,000 events; a client further behind should page the history API
  first.
- A client that cannot keep up is disconnected and resumes the same way.
- At most **5 streams per credential** and 200 per hello-control pod; the
  next one answers `429` with `Retry-After`.

### Credentials and what they see

The stream, the history and the ticket route need the `events` scope. A
personal token or service account is created with it on
**Integrations → API access** (or **AI access**); a user's session has it
already.

| Credential                                           | Receives                                        |
| ---------------------------------------------------- | ----------------------------------------------- |
| Role `viewer`, `operator`, `admin`                   | events of every extension                       |
| Role `user` (self-service)                           | only events about the extensions linked to them |
| Service account or token with an **extension limit** | only events about the extensions in the limit   |

A user's stream follows changes to the extensions linked to them within a
minute. An event with no extension (for example a call that never touched a
local extension) is only for the unrestricted credentials.

### Tickets, for clients that cannot send a header

A browser `EventSource` of another origin cannot send an `Authorization`
header. Mint a **ticket** with an authenticated call and pass it as a query
parameter:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" https://hello.example/api/v1/events/tickets
# {"ticket":"hello_tkt_…","expiresAt":"2026-10-26T09:15:00Z"}
curl -N "https://hello.example/api/v1/events/stream?ticket=hello_tkt_…"
```

A ticket is valid for 30 seconds, opens exactly one stream (it is spent when
used) and acts as the credential that minted it, with the `events` scope only.
It is accepted nowhere else. Fetch-based clients should use the bearer header
instead and keep the secret out of URLs.

### Reading the stream with fetch

```js
const res = await fetch("/api/v1/events/stream", {
  headers: { Accept: "text/event-stream", "Last-Event-ID": lastCursor },
  credentials: "same-origin",
});
const reader = res.body.getReader(); // parse frames separated by a blank line
```

## History

`GET /api/v1/events` lists stored events, paged by cursor:

| Parameter            | Meaning                                                                      |
| -------------------- | ---------------------------------------------------------------------------- |
| `types`, `extension` | as for the stream                                                            |
| `order`              | `asc` (default, oldest first, events after `after`) or `desc` (newest first) |
| `after`, `before`    | a cursor: the page starts after it (`asc`) or before it (`desc`)             |
| `limit`              | 1 to 1000, default 100                                                       |

The answer is `{"items": [...], "nextCursor": "…"}`; pass `nextCursor` back as
`after` (or `before` for `desc`) for the next page. It is empty on the last
page. Each item is the event plus its `cursor`. The MCP server exposes this
operation as the `listEvents` tool (it needs the `events` scope); the stream
and tickets are not tools.

## In the console

**Integrations → Events** is a live tail for checking what an integration will
receive. It shows the latest stored events, follows the stream, filters by type
and extension, and pauses without losing its place. A stream that goes quiet
for 45 seconds is reported as stalled and reconnected: a proxy that buffers
responses is the usual cause.

## Running it

- `hello-sip` appends to the Valkey stream `hello:events` (capped at about
  10,000 entries). `hello-control` consumes it with the consumer group `ingest`,
  stores each event once in `integration_events` (migration `00044`) and
  acknowledges it. An entry read by a replica that then crashed is taken over by
  another after 60 seconds, so nothing is lost; one stored twice is stored once.
- One blocking read per `hello-control` pod fans the live stream out in memory
  to its clients.
- The stored events are purged hourly by one replica (advisory lock
  `hello:events:purge`).
- Behind a reverse proxy the stream must not be buffered. The console's nginx
  has a dedicated location for `/api/v1/events/stream` (no buffering, one-hour
  read timeout, `X-Accel-Buffering: no`); an ingress in front needs the same.
- If Valkey is unreachable, calls keep working, new events are dropped (counted)
  and the stream answers `503` until it returns.

| Setting                      | Default | Meaning                                       |
| ---------------------------- | ------- | --------------------------------------------- |
| `HELLO_EVENT_RETENTION_DAYS` | `7`     | how long events are stored (1 to 30), control |

### Metrics

| Metric                                | Where   | Meaning                                              |
| ------------------------------------- | ------- | ---------------------------------------------------- |
| `hello_events_emitted_total{type}`    | sip     | events appended to the stream                        |
| `hello_events_dropped_total`          | sip     | events dropped: queue full or the stream unreachable |
| `hello_events_ingested_total{result}` | control | `stored`, `duplicate` or `invalid` entries consumed  |
| `hello_event_ingest_lag_seconds`      | control | time from an event's creation to its storage         |
| `hello_event_stream_clients`          | control | open event streams on this pod                       |

An alert on a rising `hello_events_dropped_total` or a growing ingest lag
catches a stalled pipeline.

## Webhooks

A **webhook endpoint** receives the events it subscribes to as signed HTTP
`POST` requests, with retries and a dead-letter queue. Manage endpoints under
**Integrations → Webhooks** or with `/api/v1/webhooks` (administrators; creating
an endpoint and rotating a secret also need the `secrets` scope, because they
return the signing secret, once).

An endpoint has a name, an `https` URL, an optional list of event types (exact
types or a prefix such as `call.*`; empty is all) and an optional list of
extensions. `POST /api/v1/webhooks` is idempotent on `(url, events)`: repeating
it returns the existing endpoint (200, null secret), which suits REST-hook
subscriptions such as Zapier's; `DELETE /api/v1/webhooks/{id}` unsubscribes.

### The request

```text
POST /your/path HTTP/1.1
Content-Type: application/json
Hello-Event-Id: evt_3f9c1b7a52d84e0a9b6c0d1e7f2a4c58
Hello-Event-Type: call.missed
Hello-Delivery: dlv_9d1c0a7e5b2f4c3d8a6e1f00
Hello-Signature: t=1761470043,v1=248a37...
```

The body is the event JSON shown above. `Hello-Event-Id` is stable across
retries and redeliveries: **dedupe on it**. `Hello-Delivery` is unique per
attempt. Events of one call may arrive out of order; order by `data.sequence`.

### Verifying the signature

`Hello-Signature` is `t=<unix seconds>,v1=<hex>` where `v1` is the
HMAC-SHA256, keyed with the endpoint's secret (the whole `whsec_...` string),
of `<t>.<raw body>`. During the 24 hours after a rotation both secrets sign and
the header has two `v1=` values; accept any. Always:

1. compute the HMAC over the **raw bytes received**, before parsing the JSON;
2. compare in **constant time**;
3. reject a timestamp more than **5 minutes** from your clock (the test event
   carries a `Date` header to compare clocks);
4. dedupe on `Hello-Event-Id`;
5. answer `2xx` quickly and do the work afterwards.

Known answer for checking your code: secret `whsec_test_secret`, `t=1700000000`,
body `{"id":"evt_1"}` gives
`248a374f50f943a28b0f6ab50faf9a7e7e29b710fa26df9fb1618b9bf8ea9c9a`.

Go:

```go
// verify returns nil when header (Hello-Signature) matches body, the exact
// bytes received, under secret, and is less than 5 minutes old.
func verify(secret, header string, body []byte, now time.Time) error {
	var ts int64
	var sigs []string
	for _, p := range strings.Split(header, ",") {
		switch k, v, _ := strings.Cut(strings.TrimSpace(p), "="); k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if d := now.Sub(time.Unix(ts, 0)); ts == 0 || d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("stale or missing timestamp")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	want := mac.Sum(nil)
	for _, s := range sigs {
		if got, err := hex.DecodeString(s); err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return errors.New("signature mismatch")
}
```

Node (use `express.raw({ type: "application/json" })` so the body is not parsed first):

```js
const crypto = require("node:crypto");

// verify throws unless header (Hello-Signature) matches rawBody, the exact
// bytes received, under secret, and is less than 5 minutes old.
function verify(secret, header, rawBody, now = Date.now()) {
  const parts = header.split(",").map((p) => p.trim().split("="));
  const t = Number(parts.find(([k]) => k === "t")?.[1]);
  const sigs = parts.filter(([k]) => k === "v1").map(([, v]) => v);
  if (!Number.isFinite(t) || Math.abs(now / 1000 - t) > 300) {
    throw new Error("stale or missing timestamp");
  }
  const want = crypto
    .createHmac("sha256", secret)
    .update(`${t}.`)
    .update(rawBody)
    .digest();
  const ok = sigs.some((s) => {
    const got = Buffer.from(s, "hex");
    return got.length === want.length && crypto.timingSafeEqual(got, want);
  });
  if (!ok) throw new Error("signature mismatch");
}
```

Python:

```python
import hashlib
import hmac
import time


def verify(secret: str, header: str, raw_body: bytes, now: float | None = None) -> None:
    """Raise ValueError unless header (Hello-Signature) matches raw_body, the
    exact bytes received, under secret, and is less than 5 minutes old."""
    parts = [p.strip().partition("=") for p in header.split(",")]
    t = next((int(v) for k, _, v in parts if k == "t" and v.isdigit()), None)
    sigs = [v for k, _, v in parts if k == "v1"]
    if t is None or abs((time.time() if now is None else now) - t) > 300:
        raise ValueError("stale or missing timestamp")
    want = hmac.new(secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256).hexdigest()
    if not any(hmac.compare_digest(want.encode(), s.encode()) for s in sigs):
        raise ValueError("signature mismatch")
```

### Delivery, retries and dead letters

- `2xx` is success. A network error, `5xx`, `408` or `429` is retried; a
  `Retry-After` is honoured up to one hour.
- Attempts: the first, then retries after **10 s, 30 s, 2 min, 10 min, 1 h, 4 h
  and 12 h** (each jittered by 20 percent): eight attempts in about 17 hours.
  The `sequence` of the schedule restarts on a redelivery.
- Any other `4xx`, a redirect (never followed) or a refused address is a
  permanent failure: the delivery is **dead** at once. `410 Gone` also switches
  the endpoint off.
- A **dead letter** (the dead-letter queue is the set of dead deliveries) stays
  30 days, at most 100,000 per endpoint, and can be redelivered one at a time
  (`POST /webhooks/deliveries/{id}/redeliver`) or all of an endpoint
  (`POST /webhooks/{id}/deliveries/redeliver-dead`), also from the console. A
  redelivery is a new attempt with the same event id. The event it carries is
  kept while a dead letter needs it.
- After **50 failed attempts in a row** an endpoint is switched off
  (`disabledReason`), the event is audited and administrators are e-mailed when
  SMTP is configured. Events are not queued for a switched-off endpoint;
  enabling it clears the count.
- At most 4 deliveries per endpoint are in flight, 32 per replica; replicas
  never send the same delivery at once (rows are leased with
  `FOR UPDATE SKIP LOCKED` and a 30 s lease, so a replica that dies loses
  nothing).
- `POST /webhooks/{id}/test` sends a signed `webhook.test` event now and shows
  the status, latency and the first kilobyte of the answer.
- `POST /webhooks/{id}/rotate-secret` makes a new secret (shown once); the old
  one keeps signing for 24 hours.

Delivery is **at least once**; ordering is best effort.

### Where webhooks may point

Receivers are reached through one SSRF-safe client. The address is checked when
the connection is made, after name resolution, so DNS rebinding cannot swap it.
Loopback, link-local (including the cloud metadata address), unspecified and
multicast addresses are never reachable, nor are private ranges by default; an
`https` URL is required. Redirects are not followed, a response is read to at
most 64 KiB and an attempt times out after 10 s.

An administrator can mark one endpoint **allow private** (an internal
receiver; `http` allowed). It then reaches only the ranges in
`HELLO_INTEGRATIONS_ALLOWED_CIDRS` (the RFC 1918 ranges by default; `none`
forbids private targets altogether; loopback and link-local space cannot be
listed). Creating or changing such an endpoint is audited.

### Settings and metrics

| Setting                            | Default  | Meaning                                           |
| ---------------------------------- | -------- | ------------------------------------------------- |
| `HELLO_INTEGRATIONS_ALLOWED_CIDRS` | RFC 1918 | private ranges an allow-private webhook may reach |

| Metric                                   | Meaning                                          |
| ---------------------------------------- | ------------------------------------------------ |
| `hello_webhook_deliveries_total{result}` | attempts: `succeeded`, `retried`, `dead`, `gone` |
| `hello_webhook_delivery_seconds`         | duration of one attempt                          |
| `hello_webhook_pending`                  | deliveries waiting or retrying                   |
| `hello_webhook_dead`                     | dead letters (alert when above 0 for an hour)    |
| `hello_webhook_endpoints_disabled`       | endpoints switched off                           |
| `hello_webhook_dead_dropped_total`       | dead letters dropped by the per-endpoint cap     |

Webhook tables are in migration `00045`; finished deliveries are purged with
the event retention, dead letters after 30 days, by one replica at a time.

## What comes next

Click-to-call (P3), recording links (P4) and the CRM connectors build on these
events.
