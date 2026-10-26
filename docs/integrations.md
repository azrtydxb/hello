# Integrations: call events

Hello publishes what happens to every call as small, ordered **call events**.
An integration reads them from a live stream or from the stored history and
uses them to show who is calling, log calls in another system or trigger an
automation. This page covers the first part of the integrations feature (spec
`.procoder/specs/integrations.md`, phase P1). Webhooks, click-to-call,
recording links and the CRM connectors build on the same events and arrive in
later phases.

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

## What comes next

Outbound webhooks with signed, retried delivery (P2), click-to-call (P3),
recording links (P4) and the CRM connectors build on these events: the ingest
transaction already has a hook point where webhook deliveries are queued.
