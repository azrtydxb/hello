# Text messaging (SIP MESSAGE)

Hello carries RFC 3428 text messages between phones, to groups, to every phone
and, per trunk, to and from the outside world (SMS over SIP).

## Routing

- A phone sends `MESSAGE sip:<target>@<domain>` through Kamailio, which routes
  it to a READY hello-sip node like an initial INVITE (pike rate-limits it per
  source IP). Kamailio relays a phone's MESSAGE to a Hello node only.
- hello-sip authenticates the device (digest, same throttle as INVITE). The
  sender is always the device's extension: a `From` naming anyone else is
  refused with 403.
- The target is an extension (every registered device gets it), a ring group
  name, a paging group code (the group's caller list applies) or an external
  number. An external number is allowed only when the sender's outbound route
  reaches a trunk with **SMS over SIP** on; otherwise 403 (404 when no route
  matches). Hello never sends to a URI the sender chose.
- Answers: `200` delivered to at least one contact, `202` stored, `400`/`413`
  bad or oversize body (700 bytes: one UDP packet), `403`, `404`, `405` in a
  call dialog, `429` over the per-sender limit (Retry-After), `503`.
- Any content type is passed through as received; text is searchable.
  Typing indications (`application/im-iscomposing+xml`) are relayed to online
  phones and never stored.
- From a trunk: a MESSAGE without credentials is accepted only from a trunk's
  source address, only if that trunk has messaging on, only from a sender that
  is not an internal extension, and only to a DID an inbound route maps to an
  extension. The phone sees `SMS <number>` and can reply to the number.

## Store and forward, HA

Messages live in PostgreSQL (`messages`), one row per recipient. A node claims
rows with `FOR UPDATE SKIP LOCKED`, so exactly one node delivers each row. A
message for an extension with no registered device waits; a REGISTER on any
node delivers it at once, a 15 s sweep catches the rest. Failed attempts back
off (15 s doubling, 6 attempts). A message not delivered within the TTL (24 h)
fails as expired; a claimed message of a dead node returns to the queue.

## Settings (admin)

`GET/PUT /api/v1/messages/settings`: on/off, TTL hours, history retention days
(default 30; old history is purged automatically, or on demand with
`POST /api/v1/messages/purge`), sends per sender per minute (30), waiting
messages kept per extension (100).

## API

| Route                                            | Role                                                                                    |
| ------------------------------------------------ | --------------------------------------------------------------------------------------- |
| `POST /api/v1/messages`                          | operator: send to an extension, group, `broadcast` (needs `confirm`) or external number |
| `GET /api/v1/messages`, `/conversations`         | admin: history and search                                                               |
| `GET/POST /api/v1/me/messages`, `/conversations` | any user linked to an extension: own extension only                                     |

`sendMessage` is an MCP tool and, for the in-product assistant, a proposal
action a person applies. Message text is not exposed to the assistant's read
tools or to MCP.

## Metrics, privacy

`hello_sip_messages_total{source,outcome}` and
`hello_sip_message_deliveries_total{via,result}`. Bodies are never logged or
audited; audit rows name the batch only.

## Phones

Provisioning writes Grandstream `P22650` and Yealink
`features.text_message.enable`, both unverified on hardware. Poly, Snom and
Fanvil have no key we can cite, so none is written. Phones need a resync to
pick up the change.
