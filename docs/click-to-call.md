# Click-to-call

Click-to-call places a call from a person's own phone to a number they chose,
without dialling it by hand. Hello rings the person's phone first. When they
answer, Hello dials the number as if the extension had dialled it at the desk
phone, and joins the two. The number is never dialled before the phone is
answered, so a stolen token or a malicious web page cannot make Hello call
anyone unless a person picks up a ringing phone (spec
`.procoder/specs/integrations.md`, phase P3, I-37 to I-39).

## From a script or an integration

`POST /api/v1/calls/originate` needs the `calls` scope.

```sh
curl -sS -X POST https://hello.example/api/v1/calls/originate \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"destination": "+971501234567", "idempotencyKey": "order-8841"}'
# 202 {"originateId":"orig_…","state":"queued","extension":"101","destination":"+971501234567",…}
```

| Field            | Meaning                                                                                                                                                     |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `destination`    | Required. Digits, `+`, `*` and `#`, at most 64. A SIP address is refused.                                                                                   |
| `extension`      | The extension whose phone rings first. Defaults to the caller's single linked extension.                                                                    |
| `deviceId`       | Ring only this device of the extension. By default every registered contact rings.                                                                          |
| `callerId`       | Optional. Honoured only if it is a number the routing allows that extension to present (its number, its external number, what the outbound route presents). |
| `autoAnswer`     | Ask the phone to answer by itself. Refused (400) unless an administrator has enabled it.                                                                    |
| `idempotencyKey` | Or the `Idempotency-Key` header. The same key from the same credential within 10 minutes returns the same request and counts against no limit.              |

Who may ring which extension:

- a user (and a viewer) only their own linked extensions;
- an operator or administrator any extension;
- a service account any extension inside its `extensionScope` (any extension
  when it has none).

`GET /api/v1/calls/originate/{id}` returns the state and `DELETE` hangs up,
whether the phone is ringing, the number is ringing or the call is connected.
Only the credential that started a request, or an operator or administrator,
can read or cancel it; anyone else gets `404`.

| State            | Meaning                                       |
| ---------------- | --------------------------------------------- |
| `queued`         | waiting for a hello-sip node                  |
| `ringing_agent`  | the person's phone is ringing                 |
| `agent_answered` | the phone answered                            |
| `dialing_target` | the destination is being dialled              |
| `connected`      | the destination answered; the two are bridged |
| `ended`          | the call ended                                |
| `failed`         | see `reason`                                  |

Failure reasons: `no_registration`, `agent_no_answer` (the phone rang for 30
seconds), `agent_busy`, `agent_dnd`, `target_busy`, `target_no_answer`,
`route_denied`, `rate_limited`, `node_unavailable` (the node that took the
request died; closed after 60 seconds) and `cancelled`.

Every state change is also a `call.originate.progress` event on the
[event stream](integrations.md), with `data.originateId`, `data.state` and,
for a failure, `data.reason`. The CDR of the bridged call has `origin: "api"`
and `originClient` naming the OAuth client, service account or token.

The operations are not tools of the MCP server: an AI agent cannot dial.

## What is refused

| Refusal                          | Answer                                                                                               |
| -------------------------------- | ---------------------------------------------------------------------------------------------------- |
| emergency numbers                | `422 emergency_not_allowed`: click-to-call carries no location, and the user may not be at the phone |
| a `callerId` not allowed         | `422 caller_id_not_allowed`; anonymous cannot be requested                                           |
| a destination the routing denies | the request fails `route_denied` before the phone rings                                              |
| a feature code or a paging code  | the request fails `route_denied`                                                                     |
| another user's extension         | `403 forbidden_role`                                                                                 |
| an account with no extension     | `403 no_extension`                                                                                   |

The routing is the same as for the desk phone: class of service, schedules,
rewrites and trunk selection all apply to the second leg.

## Limits

Limits are counted in Valkey and answer `429` with `Retry-After`. Each one is
counted in `hello_integration_rate_limited_total{scope}`.

| Limit                           | Default | Scope label         |
| ------------------------------- | ------- | ------------------- |
| per credential, per minute      | 10      | `credential_minute` |
| per credential, per day         | 200     | `credential_day`    |
| per extension, calls in setup   | 1       | `extension_setup`   |
| per extension, per hour         | 30      | `extension_hour`    |
| per destination, per 10 minutes | 3       | `destination`       |
| in all, per hour                | 600     | `global`            |

A credential refused five times in ten minutes raises one `originate.anomaly`
audit row, the sign of a stolen token being tried or a script gone wrong.
Every request is audited (`originate`, with the extension and the first digits
of the number) and so is every refusal.

## The administrator's policy

**Integrations → Overview → Click-to-call** (`GET`/`PUT
/api/v1/integrations/click-to-call`, administrators):

- **Destinations**: the outbound routes decide (default), or only internal
  numbers and numbers that start with one of the listed country prefixes
  (`+971`, `00971`).
- **Auto-answer**: whether a request may ask the phone to answer by itself
  (off by default). The phone's vendor headers are the ones the paging feature
  sends; whether a given model honours them is to be confirmed on hardware.

## From a web page: `/dial` and `tel:` links

The console page `/dial?n=<number>` works for every role. It shows the number
and the extension that will ring and waits for one click, so a link on another
site cannot place a call by itself. It then shows the call's state from the event stream (polling only while the
stream is down), and a hang up button. `hello-dialer` is registered as a
first-party OAuth client (public, PKCE, redirect
`/integrations/dialer/callback`) when `HELLO_PUBLIC_URL` is set.

**Dial** under _My phone_ has a button, _Use Hello for phone links_, that
registers the console as the browser's handler for `tel:` links
(`navigator.registerProtocolHandler`). The browser asks the user to allow it.
Chrome and Edge support it; Firefox needs a setting; `callto:` is not on the
HTML safelist and is left to the browser extension (a later phase).

## How it works

`hello-control` validates, applies the limits, stores the request in
`originate_requests` and queues it on the Valkey list `hello:originate:queue`.
Any READY `hello-sip` node takes it within 250 ms, sends leg A (an INVITE with
no SDP and the destination as the caller, so the phone shows why it rings) to
the extension's contacts and waits for an answer. The phone's 200 carries its
SDP offer. That offer, with the phone's dialog (Call-ID, tags, contact, route
set), enters the ordinary call path as an INVITE the phone "placed" to the
destination, so every behaviour of a normal call follows: routing, rewrites,
anchoring, recording, hold, transfer, voicemail, events and in-call HA. The
engine's answer to its "caller" becomes the ACK of the phone's 200; a failure
becomes a BYE. A sweeper in `hello-control` closes requests whose node died
(`node_unavailable`), copies the states the nodes post into the rows and
purges finished rows after 30 days.

Metrics: `hello_originate_total{result}` (`connected` or the failure reason)
and `hello_originate_setup_seconds` on `hello-sip`;
`hello_integration_rate_limited_total{scope}` on `hello-control`. Migration
`00047` adds `originate_requests`, `originate_settings` and the CDR columns
`origin` and `origin_client`.
