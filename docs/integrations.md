# Integrations: call events, webhooks and directory sync

Hello publishes what happens to every call as small, ordered **call events**.
An integration reads them from a live stream or from the stored history and
uses them to show who is calling, log calls in another system or trigger an
automation. This page covers the first part of the integrations feature (spec
`.procoder/specs/integrations.md`, phases P1 and P2). Click-to-call,
recording links and the CRM connectors build on the same events and arrive in
later phases. [Webhooks](#webhooks) push the events to your server.

## What is emitted

| Event                     | When                                                                                                                                                                                                                                    |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `call.ringing`            | An extension starts ringing. A call that rings several extensions (a ring group) yields one event per ringing extension.                                                                                                                |
| `call.answered`           | An extension answered. The calling extension of an internal or outbound call gets one too.                                                                                                                                              |
| `call.ended`              | The call ended, for each extension it involved.                                                                                                                                                                                         |
| `call.missed`             | The call reached an extension and nobody answered. A call answered by another member of a ring group is not missed for the others.                                                                                                      |
| `call.recording.ready`    | A recording of the call was stored. It is stored after the call ends, so it follows `call.ended`. Carries `recordingId`, `durationMs`.                                                                                                  |
| `call.completed`          | The call detail record was stored. Carries `cdrId`; this is the point to log the call elsewhere.                                                                                                                                        |
| `call.originate.progress` | A click-to-call changed state ([click-to-call](click-to-call.md)). Carries `originateId`, `state` and, for a failure, `reason`.                                                                                                         |
| `voicemail.received`      | A caller left a voicemail message in the box of an extension (one event per box for a voicemail group). Carries the caller in `from`, the box extension in `extension` and `to`, and `durationMs`. The audio is never part of an event. |

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

## Recording links

A recording link lets someone without a Hello session, such as a CRM user,
play one call recording. Hello serves the audio itself (the object store is
cluster-internal, so a presigned URL could not be followed from outside).

```sh
curl -X POST https://hello.example/api/v1/recordings/42/links \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"ttlHours": 720, "maxUses": 0}'
# {"id":7,"recordingId":42,"url":"https://hello.example/r/<token>.wav","expiresAt":"...","maxUses":0}
```

- Scope `recordings`. A user, or a token or service account with an
  `extensionScope`, may only link recordings of calls involving its own
  extensions (`403` otherwise); a viewer is limited to the extensions linked to
  them, and operators, administrators and unrestricted service accounts may
  link any recording. The body is optional: `ttlHours` is 1 to 8760 (default
  720, 30 days) and `maxUses` is 0 for unlimited.
- The URL is shown once. The token is 256 random bits; only its SHA-256 is
  stored. Anyone holding the URL can play the recording until it expires, so
  treat it as a secret. The `.wav` suffix is there for CRMs that play only
  files with an audio extension and need HTTPS.
- `GET /r/<token>.wav` needs no credential, supports `Range`, and answers
  `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer` and
  `X-Robots-Tag: noindex`. An unknown, expired, revoked or used-up link is the
  same `404`. Each token may be requested 30 times a minute from one address
  (`429` with `Retry-After`, per replica). `maxUses` counts requests, so a
  player's `Range` requests each count.
- `DELETE /api/v1/recording-links/{id}` revokes a link (the creator, or anyone
  allowed to link that recording). Deleting the recording deletes its links.
- Creating and revoking a link are audited (`create`/`revoke`
  `recording_link`, with the credential as the actor). The token is never
  logged or audited.
- The console's nginx proxies `/r/` to hello-control; an ingress in front must
  send `/r/` there too. Recording notice and consent stay governed by
  `HELLO_MEDIA_RECORDING_NOTICE`: a link means a recording leaves Hello's
  storage in the sense that whoever has the link can play it.

Metrics: `hello_recording_link_hits_total{result}` (`ok`, `unknown`, `revoked`,
`expired`, `used_up`, `rate_limited`, `storage_error`) and
`hello_recording_links_active`.

## Finding calls and recordings

```sh
curl -H "Authorization: Bearer $TOKEN" \
  'https://hello.example/api/v1/lookup/calls?number=%2B3211111222&from=2026-11-01T00:00:00Z&recorded=true'
```

- `GET /api/v1/lookup/calls` (scope `calls`) and
  `GET /api/v1/lookup/recordings` (scope `recordings`) take the same filters:
  `number`, `extension`, `from`, `to` (RFC 3339, on the call's start),
  `callId` (the id call events carry, or the SIP Call-ID), `before` and
  `limit` (1 to 200); calls also take `recorded=true`. Results are newest
  first, `next` is the cursor for the following page.
- `number` ignores spaces, dashes and brackets and compares digits only; a
  number of eight or more digits also matches by its last eight digits, so
  `+32 11 111 222` finds a call recorded as `011 111 222`. It matches the source,
  the destination and the dialled number of the call.
- Each call carries `recordingId` when it was recorded, ready for
  `POST /recordings/{id}/links`. A recording appears once its audio is stored.
- The restriction is applied in the query, not after it: a user, or a
  credential with an `extensionScope`, only ever sees calls involving its own
  extensions, and asking for another extension is `403`. The viewer, operator
  and admin roles and unrestricted service accounts see all calls.
- Both lookups are MCP tools (`lookupCalls`, `lookupRecordings`); creating a
  link is not.

## What comes next

Outbound webhooks with signed, retried delivery (P2), click-to-call (P3)
and the CRM connectors build on these events: the ingest
transaction already has a hook point where webhook deliveries are queued.

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
	_, _ = fmt.Fprintf(mac, "%d.", ts)
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
    want = hmac.new(
        secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256
    ).hexdigest()
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

Webhook tables are in migration `00058`; finished deliveries are purged with
the event retention, dead letters after 30 days, by one replica at a time.

## Directory sync (Active Directory and LDAP)

A directory connector reads users from an Active Directory or LDAP server and
keeps Hello's users in step with them: it creates a user, links it to the
extension an attribute names, sets the role and ring-group membership from
group rules, and disables the user when the directory drops or disables the
account. Hello never writes to the directory and never deletes a user.
Everything is under **Integrations > Directory** (administrators only) and
`/api/v1/directory/*`.

### Setting up a connector

1. Create the connector with the server URL (`ldaps://host:636`, or
   `ldap://host:389`, which always uses StartTLS), a bind DN with read access,
   the bind password, the base DN and, if needed, a user filter. The default
   filter is `(&(objectCategory=person)(objectClass=user))`; it must also match
   disabled accounts, which are decided from `userAccountControl`.
2. **Test** it: Hello connects, verifies the certificate, binds and reads one
   page, and shows the certificate, the user count, a sample of 20 users with
   their raw and mapped values, and the groups under the group base.
3. **Preview** a sync. Nothing changes; the run lists, for every user, what
   would happen (`create`, `update`, `adopt`, `disable`, `enable`, `conflict`,
   `skip`) with the before and after of each field.
4. **Apply** the preview. A schedule (default 60 minutes, 0 for manual only)
   starts only after a run has been applied by hand.

The bind password is sealed with `HELLO_SECRET_KEY` (bound to the connector)
and is never returned, logged, audited or put in an error. Changing the server
or the bind DN requires entering it again, so a stored password cannot be sent
to another host. Setting it needs the `secrets` scope. Certificate verification
is always on: a private CA goes in the connector's CA certificate (a public
certificate), a different certificate name in the server name. Plain `ldap://`
without StartTLS is refused (`400 tls_required`); the lab switch
`HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP=true` allows it and logs a warning on
every use. The server must be inside `HELLO_INTEGRATIONS_ALLOWED_CIDRS` (RFC
1918 by default) or public; loopback and link-local addresses are never
reachable. Referrals are not followed; TLS 1.2 is the minimum.

### Mapping

Each of username, email, display name and extension reads an ordered list of
source attributes (the first value wins) through optional transforms: keep only
digits, keep the last N characters, or a regular expression with one capture
group. The extension must come out as 2 to 10 digits or the user is reported as
a conflict (`extension_invalid`); it is not an error for the run. The defaults
are the Active Directory ones (`userPrincipalName`, `mail`, `displayName`,
`ipPhone` then `telephoneNumber`, `userAccountControl` bit 2 for disabled). A
user is linked by the directory's stable id (the AD `objectGUID`, shown in its
canonical form), never by email.

The mapped fields are the managed fields: on a user the connector manages they
are read-only in the console and the API (`409 managed_by_directory`), with a
lock and the connector's name. Unmapped fields stay editable. A directory user
has no local password.

### Modes

- **authoritative** (default): the directory overwrites the mapped fields on
  every run.
- **create_only**: the directory seeds a field once; Hello owns it afterwards.
- **link_only**: never creates users; links existing local users by email, then
  username, and manages them.

### Groups

Group rules give members of a source group (matched on its stable id) a role or
a ring group. The highest matching role wins and users matching none are plain
`user`s. A rule granting `admin` is ignored unless the connector's **allow admin
from directory** switch is on. Ring-group rows a sync adds are tagged with the
connector; it removes only those and never touches a manual member. Nested
membership uses the AD in-chain matching rule and is off by default (slow on
large forests).

### Auto-provisioning from a number range

A connector can give a user an extension of its own (off by default; the
**Auto-provisioning** part of the connector form, or `autoProvision` in the
API). Set the first and the last number of a range, for example 2000 and 2999
(the same number of digits, at most 100,000 numbers). A user whose extension
attribute is empty, or names an extension that does not exist, then gets the
next free number of the range, and with it:

- an extension named after the user's display name (or username);
- a voicemail box, switched on, that mails messages to the user's email
  (switch **voicemail** off to create the box switched off);
- one SIP device named after the number (switch **device** off to skip it). Its
  secret is generated and shown nowhere: rotate it from the device page when
  you set up a phone, or provision the phone, which binds the device.

A number is free when nothing in the dial space has it: no extension, ring
group, IVR menu, conference room, queue or voicemail group number, no park
slot, and no feature or paging code that overlaps it. The whole configuration
is checked as for an extension created by hand. **Only members of these
groups** limits provisioning to source groups (stable ids, as in the group
rules; needs the group base DN); other users are created without an extension
as before. The preview lists such users with the reason `auto_provisioned` and
the `extension`, `device` and `voicemail` it would create.

The number is allocated when the change is applied, in the same transaction as
the user and under the configuration lock, so two runs and an administrator's
own edits never take the same number. When the number the preview showed was
taken in between, the next free one is used and the run's row shows the number
actually given. A full range is the conflict `range_exhausted`: that user is
left unchanged and the others go on; widen the range and the next run creates
it. A number stays with its user: re-runs never renumber, a deleted extension
comes back with the same number at the next full run (in `create_only` mode a
user is provisioned once and Hello owns the extension afterwards), and a
disabled user keeps its extension and voicemail like any other. While
provisioning is on, the extension's name and the voicemail email follow the
user's display name and email, unless an administrator changed them. Hello
never writes the number back to the directory. The connector's runs page shows
how full the range is, the next number and who has a number from it
(`GET /api/v1/directory/connectors/{id}/range`).

### What a run never does

- It never deletes a user. A user that is inactive in the directory is disabled
  at once; one that is absent from two complete runs in a row (1 to 10,
  configurable) is disabled. Disabling ends the user's sessions, revokes their
  tokens and grants, and by default switches off their devices; the extension,
  voicemail and call records stay. A user that comes back is enabled again, with
  those devices.
- It never adopts, demotes or disables a local admin, and never leaves the PBX
  without an enabled admin (`last_admin`).
- It never adopts a local user by itself: a local user matched by email, then
  username, is listed as `adopt` and changed only when the administrator ticks
  it on apply.
- It leaves a conflicted user unchanged and goes on with the rest. Conflicts
  are `extension_linked` (the administrator may override it on apply, which
  moves the extension), `duplicate_extension`, `extension_missing`,
  `extension_invalid`, `extension_kind` (a door station), `email_in_use`,
  `username_collision`, `username_invalid`, `claimed_by_other_connector`,
  `range_exhausted` (auto-provisioning) and `last_admin`.
- It halts as `needs_confirmation`, applying nothing, when it would disable more
  than 20 percent of the linked users (and more than 5), or when the source
  returns no users although users are linked. A person reviews the changes and
  confirms.
- Applying a preview re-reads the directory; if the preview is older than 15
  minutes or the diff hash differs it answers `409 stale_preview` with a fresh
  preview.

### Running it

One replica runs a connector: every `hello-control` replica ticks, and the one
that takes the session advisory lock `hello:dirsync:<id>` runs it; the lock goes
with its connection, so another replica takes over. A run left `running` by a
dead replica is closed as `failed: replica_lost`. A run has a timeout (15 min)
and a user cap (20,000) after which it fails visibly. A failure is classified
(`auth_failed`, `tls_untrusted`, `unreachable`, `timeout`, `quota`,
`bad_filter`, `user_cap_exceeded`) with a short detail that never echoes a
credential or the server's text; after three in a row the administrators are
e-mailed when SMTP is set up. Nothing is changed by a failed run. Changes are
kept 90 days. Each user-level change is audited with the actor
`directory:<connector name>`; each run, test, apply and confirmation with the
administrator.

| Setting                                | Default  | Meaning                                                    |
| -------------------------------------- | -------- | ---------------------------------------------------------- |
| `HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP` | off      | lab only: allow `ldap://` without StartTLS, with a warning |
| `HELLO_INTEGRATIONS_ALLOWED_CIDRS`     | RFC 1918 | private ranges a directory server may be in                |

| Metric                                                      | Meaning                                        |
| ----------------------------------------------------------- | ---------------------------------------------- |
| `hello_directory_sync_runs_total{kind,result}`              | runs by kind and result                        |
| `hello_directory_sync_duration_seconds{kind}`               | duration of a run                              |
| `hello_directory_changes_total{kind,action}`                | changes by action                              |
| `hello_directory_conflicts{connector_kind}`                 | conflicts of the latest run                    |
| `hello_directory_last_success_timestamp_seconds{connector}` | alert when older than three schedule intervals |
| `hello_directory_provisioned_total{connector_kind}`         | extensions created by auto-provisioning        |

Tables are in migrations `00093` and `00101` (auto-provisioning).

## Directory sync: Google Workspace and Microsoft Entra ID

The same engine runs two more connector kinds, chosen when the connector is
created: **Google Workspace** (the Admin SDK Directory API) and **Microsoft
Entra ID** (Microsoft Graph). Preview, apply, modes, group rules,
auto-provisioning from a number range, the safeguards and the schedule are
those of the LDAP connector above; only the source differs. A Google or
Entra user whose work phone names no existing extension gets a number from
the connector's range when auto-provisioning is on (the group filter uses
Google group ids or Entra group object ids). An adopted local user loses its
local password and gets a number like a created one; a break-glass
administrator is never adopted or provisioned. An Entra incremental run
provisions only the users in the delta; a provisioned extension that was
deleted comes back at the next full read. Every call is read-only, over HTTPS to the provider's fixed
endpoints, and a paging link that leaves the provider's host is refused. A
`429` or `503` with `Retry-After` is waited out inside the run's budget (at
most 12 retries, 2 minutes per wait, 6 minutes in total); after that the run
fails as `quota`.

The credential (a service account key, a client secret, or a certificate's
private key) is sealed like the bind password, never returned, and needs the
`secrets` scope. The API takes it in the connector's `credential` field
(`bindPassword` is the LDAP spelling; a request may carry one of them, not
both).

### Google Workspace

Users come from `users.list` and groups from `groups.list` and
`members.list` (with derived membership when nested groups are on). Google
has no change-since filter, so every run is a full read. A user is linked by
Google's user `id`; the defaults map `primaryEmail` to username and email,
`name.fullName` to the display name, the digits of the `work` phone to the
extension, and a suspended or archived user to inactive. The customer is
`my_customer` unless set; an optional domain and a `users.list` query (for
example `orgUnitPath='/Sales'`) narrow the read.

Hello asks only for these scopes, whatever the client was granted:

- `https://www.googleapis.com/auth/admin.directory.user.readonly`
- `https://www.googleapis.com/auth/admin.directory.group.readonly` (group
  rules only)
- `https://www.googleapis.com/auth/admin.directory.group.member.readonly`
  (group rules only)

There are three ways in:

1. **Service account with a read-only admin role** (default, recommended). In
   Google Cloud create a service account and a JSON key. In the Admin console
   create a custom admin role with the read privileges for users and groups
   only, and assign it to the service account's e-mail address. Paste or
   upload the JSON key as the credential. No user is impersonated.
2. **OAuth client with administrator consent.** Create an OAuth client (web
   application) in your own Google Cloud project, register Hello's directory
   consent redirect URI (shown on **Integrations > Single sign-on**,
   `<HELLO_PUBLIC_URL>/api/v1/directory/oauth/callback`), enter the client id
   and, as the credential, the client secret. Save, then press **Grant
   consent**: a Google administrator signs in and approves the three scopes.
   The refresh token Google returns is sealed with the secret. A grant with any
   scope outside the list above is refused. Replacing the secret drops the
   refresh token, so consent is granted again. Needs `HELLO_PUBLIC_URL`.
3. **Domain-wide delegation** (offered, discouraged). The service account
   impersonates the administrator named as the subject. In the Admin console
   authorise the service account's client id for exactly the scopes above;
   the page shows them. Anyone holding the key can then read the directory
   as that administrator, which is why the first way is preferred.

### Microsoft Entra ID

Register an application in your tenant, grant it the **application**
permissions `User.Read.All` and `GroupMember.Read.All` with administrator
consent, and enter the tenant id and the application (client) id. The tenant
is pinned: `common`, `organizations` and `consumers` are refused. The
credential is either a **client secret** or a **certificate**: upload the
certificate (public part) to the app registration, and give Hello the
certificate PEM in the connector and its RSA private key as the credential.
Hello signs a client assertion with it (headers `x5t` and `x5t#S256`); the key
must belong to the certificate.

A user is linked by its object id. The defaults map `userPrincipalName` to the
username, `mail` (then `userPrincipalName`) to the email, `displayName`, the
digits of the first `businessPhones` entry to the extension, and
`accountEnabled` to active. Guests (`userType` Guest) are skipped unless
**include guests** is on. Group rules read each group's `transitiveMembers`.

A full run lists `/users` 999 at a time and ends by taking a delta link
(`$deltatoken=latest`), which is kept with the connector. An incremental run
follows that link and re-reads only the changed users. It falls back to a full
read, named in the run's reason, when:

| Reason           | When                                                        |
| ---------------- | ----------------------------------------------------------- |
| `delta_expired`  | Graph answers `410 Gone` (the token expired or was reset)   |
| `delta_removed`  | the delta reports a removed user: absence needs a full list |
| `delta_large`    | more than 1,000 users changed                               |
| `groups_changed` | the membership of a group used by a rule changed            |

The daily full read and the absence rule of the LDAP connector apply as well.

## Single sign-on

People sign in with their Google Workspace or Microsoft account through an
OAuth client you own (OpenID Connect, authorization code with PKCE). It is set
up on **Integrations > Single sign-on** (administrators only) and
`/api/v1/auth/sso`; it needs `HELLO_PUBLIC_URL`, the address people reach
Hello at, and Valkey. The sign-in page then shows a button for each enabled
provider.

### Setting up a provider

- **Google:** create an OAuth client (web application), register the redirect
  URI the page shows (`<HELLO_PUBLIC_URL>/api/v1/auth/oidc/google/callback`),
  enter the client id and secret, and list your Workspace domains. Only an
  account whose `hd` claim is one of them and whose email is verified is
  accepted.
- **Microsoft Entra ID:** register a web application with the redirect URI
  `<HELLO_PUBLIC_URL>/api/v1/auth/oidc/entra/callback`, create a client
  secret, and enter the tenant id, client id and secret. The issuer is pinned
  to your tenant; `common` and `organizations` are refused.

**Test** fetches the provider's discovery document and keys. The client
secret is sealed with `HELLO_SECRET_KEY`, never returned, and setting it needs
the `secrets` scope.

Every ID token is checked for its signature against the provider's published
keys, the issuer, the audience (and `azp`), the expiry and the nonce; `state`
and `nonce` are single use and bound to the browser by a short-lived cookie.
Any failure signs nobody in. A user with two-factor authentication still
enters a code after the provider.

### Who signs in as whom

Hello never matches an account by email alone except in one narrow case. In
order:

1. An identity already linked to a user.
2. A user a directory connector of the same provider synced, by its id
   (Google user id, Entra object id), from the same tenant.
3. Google only: exactly one synced user, not an administrator, with that
   email address.
4. Otherwise, with **create a user on first sign-in** on, a new `user`-role
   account without a local password. Off by default.

Local users and administrators link their account themselves, signed in, on
**My phone > Profile** (**Link Google**, **Link Microsoft**); an administrator
can unlink an identity on the user's dialog. A directory connector whose users
sign in by single sign-on cannot be enabled until its provider is.

### Directory users and passwords

A user a connector manages has no local password and the password form never
falls back to one: changing or resetting it answers `409 managed_by_directory`.
Each connector says how its users sign in (**How these users sign in**):
single sign-on with its provider, or, for Active Directory and LDAP, an **LDAP
bind**: the sign-in page's password is checked by binding to the directory as
that user (found by their stable id). A bind login is throttled after 5
failures per user and per address in 10 minutes, and the answer never says
which check failed. Hello's own lockout is in front of the directory's.

### Requiring single sign-on and break-glass

With **Require single sign-on** on, the password form only lets in
**break-glass** administrators: enabled local administrators flagged on the
Users page. If the identity provider is down, one of them signs in and
switches it off. It cannot be turned on with no break-glass administrator
(`409 no_break_glass`), and the last one cannot be disabled, demoted, deleted
or unflagged while it is on (`409 last_break_glass`). A directory sync never
adopts, updates or disables a break-glass user. Give these accounts long
passwords and two-factor authentication.

| Metric                                     | Meaning                                                                                                                            |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `hello_oidc_logins_total{provider,result}` | single sign-on attempts: `ok`, `denied`, `bad_state`, `bad_token`, `no_user`, `disabled`, `already_linked`, `unavailable`, `error` |
| `hello_ldap_bind_logins_total{result}`     | LDAP bind logins: `ok`, `refused`, `throttled`, `error`                                                                            |

Each sign-in, failure (without the identity that tried), link, unlink and
settings change is audited. Tables are in migration `00102`.

### SCIM

A connector of kind **SCIM** is not read: an identity provider (Entra ID, Okta,
JumpCloud) pushes users and groups to `/scim/v2` with a `hello_scim_` token,
and each request runs the same mapping, group rules, modes, conflicts and
deprovisioning rules as a sync, applied at once. Its guard counts SCIM disables
per rolling hour and pauses the connector instead. See [SCIM](scim.md).

## What comes next

Click-to-call (P3), recording links (P4) and the CRM connectors build on these
events. [Zapier and other automation tools](zapier.md) use the webhooks above.

[Slack notifications](slack.md), [n8n and Make](n8n-make.md) and
[Outlook and Google contacts and calendar](contacts-calendar.md) use the events
and the connector framework above.

## Microsoft Teams

Teams Direct Routing needs a Microsoft-certified SBC, and Hello is not one.
The supported way to connect Hello and Teams is an SBC (or a
Direct-Routing-as-a-Service provider) in front of Hello, with Hello seeing it
as a trunk of the **SBC peer** profile. There is no Teams code in Hello and no
Microsoft account is configured in it. See [teams.md](teams.md) for the call
flow, the Hello trunk, the number plan, the Teams-side steps, emergency calls
and the support boundary.
