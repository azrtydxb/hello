# Messaging inbox

The inbox is where customers' text messages are answered. A customer texts a
number of yours, the text becomes a **conversation**, and a person answers it
from the console. It builds on the SIP text messaging of
[messaging.md](messaging.md): a SIP trunk that carries SMS as SIP MESSAGE is
the first (and, so far, only) **channel**.

This is the first phase of the messaging channels. What works now:

- inbound and outbound SMS through a SIP trunk whose carrier carries SMS as SIP
  MESSAGE (**it works only where your carrier supports that**; Hello names no
  carrier as supported);
- the inbox for supervisors and agents, with assignment by hand and claiming,
  notes, tags, states, search, live updates and notifications;
- Hello's own opt-out handling, retention and erasing a customer.

- sending SMS through a **Twilio** account (see "Sending through Twilio"):
  outbound only.

Not yet: receiving texts from a carrier's web API (needs an Internet-facing
address), Telnyx, WhatsApp, the chat widget, texts on desk phones, queues and
automatic assignment, and AI. None of the providers that need an
Internet-facing address is possible while Hello is reachable only on the LAN.

## Setting it up

1. Turn on **SMS over SIP** on the trunk (Trunks), as in
   [messaging.md](messaging.md).
2. **Messaging > Channels > Add channel**: a name and that trunk. **Test**
   checks that the trunk exists, is enabled and has SMS over SIP on, and that
   the channel has a number. It sends nothing.
3. **Add number**: the number customers text, as the carrier dials it to you.
   `+3270123456` and `3270123456` find the same number (the last nine digits
   decide when the exact form differs). A text to a number that is not in the
   inbox, or whose channel or number is off, takes the normal route to an
   extension, exactly as before.
4. The inbox appears in the navigation once an enabled channel has an enabled
   number: **Messaging > Inbox** for operators and administrators, **My phone >
   My inbox** for everyone else.

A channel or a number that has conversations cannot be deleted (`409`):
retention, or erasing the customer, removes them first.

## Sending through Twilio

A **Twilio** channel sends texts through your own Twilio account. Hello has no
shared account or relay: the account, its numbers and its fees are yours.

**What works, and what does not.** Hello is reachable only on the LAN, and
Twilio delivers an inbound text by calling a public webhook. So a Twilio
number **cannot receive** texts yet, and a customer's reply to a text from it
does not arrive in the inbox. A conversation on a Twilio number starts with a
**first-contact** text (below), and everything after that is Hello's outbound
side: the agent's texts and their delivery status. Receiving needs the public
listener, which is not built; do not plan on replies arriving.

1. **Messaging > Channels > Add channel**, carrier **Twilio**: the account SID
   (`AC` and 32 hex digits), the auth token, and optionally a Messaging Service
   SID (`MG...`) when texts should leave through its sender pool instead of the
   number. Hello checks the account with Twilio before it saves anything (a
   refused token is `422`, an unreachable Twilio `502`), then seals the SID and
   token with `HELLO_SECRET_KEY`. **The token is never shown again**, not in
   the API, the console, a log or an audit row. To change it, enter the SID and
   the token together; they replace the stored ones after Twilio accepts them.
   **Test** asks Twilio whether the stored credentials work and the numbers can
   be listed; it sends nothing. `GET /api/v1/messaging/channels/{id}/numbers`
   lists the numbers of the account.
2. **Add number**: the Twilio number in international format
   (`+3270123456`). **Texts per minute** (default 60) caps how fast a number
   sends; the rest wait queued.
3. Before go-live, start the **A2P 10DLC** brand and campaign registration (and
   toll-free verification if you want it) in your Twilio account. Hello only
   sends; the registration, the carrier fees and what Twilio filters are
   Twilio's. Unregistered US traffic is filtered or surcharged by carriers.

**First contact.** Off by default. An administrator turns it on in **Messaging
settings** ("Allowed with a consent attestation"). An operator or
administrator then starts a conversation from the Inbox (**Text a customer**,
or `POST /api/v1/inbox/conversations`): one customer number in international
format, one message, the carrier number it leaves from, a confirmation that the
customer agreed to be texted and a note saying how. The attestation and the
note are stored on the conversation and the audit log records who sent it,
never the number or the note. Hello refuses a number that opted out (`422`), a
customer who already has an open conversation on that number (`409`, reply
there), and a number that is not in international format (`400`). It is rate
limited per agent (default 20 an hour) and per number (default 200 a day):
`429`. There is no list, import or multi-recipient form, and there never will
be: Hello is not a campaign tool.

**How a text is sent, and what its state means.** A text is stored **queued**
in the same transaction that checks the opt-out, then a worker claims it
(**sending**) and calls Twilio once. It is sent **at most once**: if Twilio's
answer is lost (a timeout, a dropped connection, a 5xx), Hello cannot know
whether Twilio has the text, so it is never sent again; after five minutes it
becomes **unknown** ("may have been delivered"). Otherwise it is **sent** when
Twilio accepts it, then **delivered** or **failed** (with Twilio's error code)
when Twilio reports it. There are no status callbacks on the LAN, so Hello
**polls** Twilio for each text: every 5 seconds at first, backing off to every
5 minutes, then every 30 minutes after the first hour, and it stops at the
final state or after 48 hours. A text still queued after an hour (the channel
is off) fails with `not_sent`. A reply can be up to 1600 characters (Twilio
segments it); media is not sent.

**Opt-out.** Twilio blocks a recipient who replied STOP and fails the send with
error 21610. Hello records that as an opt-out on the number (source
"carrier"), so its own list and the composer agree, and nobody can text that
customer from that number.

**When the carrier fails.** Five failed or lost sends in a row mark the channel
**degraded** (a banner; sends continue and a good send clears it). A refused
token (`401`) marks it **credentials invalid**: the text fails, later texts
wait queued and are not sent, and the channel accepts no new ones until a
working token is entered.

Metrics: `hello_msg_outbound_total{provider,result}` (sent, failed, unknown).
The send, status and settle workers run on every control replica; the store
hands each text to one attempt, so two replicas never send the same text.

## Conversations

One conversation is one customer on one of your numbers while it is open. A new
text on a **resolved** conversation reopens it if it was resolved less than
`reopenDays` ago (default 7), otherwise it starts a new conversation. States
are open, pending (waiting on the customer), snoozed (until a time), resolved
and spam.

New conversations arrive **unassigned**: every operator and administrator sees
them in the pool. Anyone who may see one can **take** it (claim). A claim is a
compare-and-set in PostgreSQL, so two agents, or two control pods, cannot both
win: the second gets `409`. **Replying** to an unassigned conversation takes it,
and replying to a resolved one reopens it. A supervisor can assign any
conversation to any agent, or return it to the pool; an agent can hand over one
that is theirs.

What each role reaches:

| Role                | Reaches                                                                                          |
| ------------------- | ------------------------------------------------------------------------------------------------ |
| `viewer`            | Nothing: message text is customer data.                                                          |
| `user`              | Their own inbox only: the conversations assigned to them and those they replied to.              |
| `operator`, `admin` | Every conversation (`/api/v1/inbox/...`); their own inbox too (`/api/v1/me/inbox/...`).          |
| `admin`             | Also the channels, numbers, opt-outs, settings and erasing a customer (`/api/v1/messaging/...`). |

Access needs the `messages` scope as well as the role. A supervisor opening a
conversation that is not assigned to them leaves an audit row (once an hour per
conversation); it names who, never the content.

A reply is a text from the number the customer texted, sent through the
channel's trunk by the SIP text queue of [messaging.md](messaging.md). A long
reply (up to 2000 bytes) is split into several texts of at most 700 bytes. The
reply's state is **sending** until the trunk answers: **sent** when it accepts
the text, **failed** when it refuses or the text expires, **unknown** if the
outcome was lost. There are no delivery receipts on this path, so a reply is
never "delivered". The composer shows how many texts a message makes (GSM-7:
160 characters, then 153 a part; any other character makes it UCS-2: 70, then
67). Sending the same `clientMsgId` again returns the first message and sends
nothing more.

Internal **notes** are visible to agents and supervisors and are never sent.

## Opt-out (STOP)

Hello handles opt-out itself, whatever the carrier does:

- a text that is, as a whole, one of `stop`, `stopall`, `unsubscribe`, `cancel`,
  `end` or `quit` (any case, punctuation ignored; configurable per number)
  opts the customer out. "Please stop calling me at 5" does not;
- `start` or `unstop` opts them back in; `help` or `info` is answered with the
  number's help text if it has one;
- an opted-out customer cannot be texted by anyone (`422 opted_out`): not by an
  agent, not by a supervisor. The composer is disabled and says why. Only the
  customer's START, or an administrator recording the customer's consent (with
  a note, audited), clears it;
- Hello answers the first opt-out once with the number's opt-out text (or a
  neutral sentence);
- opt-outs are kept as an HMAC of the number under a key derived from
  `HELLO_SECRET_KEY`, so an **erased** customer who had opted out is still not
  texted again. This is the only residue of an erased number.

The Opt-outs page lists them, searches by the end of the number, records one by
hand and records re-consent.

## Retention, erasing, privacy

Bodies are stored as plain text in PostgreSQL (the volume's encryption is the
platform's job) with a full-text index. A resolved or spam conversation is
deleted `retentionSmsDays` (default 365) or `retentionChatDays` (default 90)
after it was resolved, in batches of 500, by one control replica. An open
conversation is never deleted by age. Snoozed conversations come back when
their time passes.

**Erase a customer** (Messaging settings, or `POST /api/v1/inbox/erase`)
deletes every conversation, message and note with a number on every channel in
one transaction, keeps only the opt-out hash, and returns a report of counts.
The carrier keeps its own logs and database backups keep the rows until they
expire: keep backups no longer than your longest erasure deadline.

Message text and customer numbers never appear in logs, metric labels or audit
rows; logs and audit rows carry conversation ids and counts. Message text is
not exposed to MCP or the AI assistant: every inbox operation is excluded
except the counts (`getInboxStats`).

## Live updates and notifications

The console holds one server-sent event stream, `GET
/api/v1/me/inbox/stream`, for the signed-in user. Events carry only ids
(`conversation.new`, `conversation.assigned`, `conversation.updated`,
`message.in`, `message.sent`, `message.status`); the page reads the rest through
the API. Event ids come from PostgreSQL (`conversation_events`, kept 7 days),
so a stream that reconnects with `Last-Event-ID` misses nothing, on whichever
control pod it lands. A user only receives events about conversations that
concern them (their own, or the unassigned pool for supervisors). The console's
nginx has its own location for the stream with buffering off; a different proxy
in front needs the same.

The page also:

- shows an unread badge in the navigation and in the page title;
- can show a desktop notification ("New message from ...", never the text) once
  the user allows it, and play a short sound (a toggle);
- emails an agent who has an email address and no inbox open for two minutes,
  at most once per conversation per 15 minutes, **without the text or the
  number**. It needs SMTP ([user-accounts.md](user-accounts.md)); the switch is
  in the settings.

## API

| Route                                                                | Role and scope        |
| -------------------------------------------------------------------- | --------------------- |
| `/api/v1/messaging/channels` (+ `/{id}`, `/{id}/test`)               | admin, `admin`        |
| `/api/v1/messaging/addresses` (+ `/{id}`)                            | admin, `admin`        |
| `/api/v1/messaging/settings`                                         | admin, `admin`        |
| `/api/v1/messaging/optouts` (+ `/{id}/reconsent`)                    | admin, `admin`        |
| `POST /api/v1/inbox/erase`                                           | admin, `admin`        |
| `/api/v1/inbox/conversations...`, `/api/v1/inbox/stats`              | operator, `messages`  |
| `/api/v1/me/inbox/conversations...`, `/status`, `/agents`, `/stream` | any agent, `messages` |

Conversations list with `view` (`all`, `mine`, `unassigned`), `status`, `q`
(number, name or text), `channelId`, and a cursor. Every operation is in
[the OpenAPI document](../internal/api/openapi.json).

## Metrics

`hello_msg_inbound_total{provider,result}` (stored, duplicate, not_text,
failed), `hello_msg_optout_total{source}`, `hello_msg_purged_total{kind}`,
`hello_msg_unassigned` and `hello_msg_unassigned_oldest_seconds`. No label
carries a number, a name or text.

## HA

Every control and SIP replica can run every part: an inbound text is ingested in
one transaction (an event ledger makes a delivery seen once, on any pod; one open
conversation per number and customer is made under a unique index), claims and
assignments are compare-and-set, retention and notification claims use
`SKIP LOCKED` or a Valkey lease. Nothing is held only in memory, so a pod
restart loses nothing. Valkey is not needed for the inbox.

## Lab checklist (real carrier)

Which carrier delivers inbound SMS as SIP MESSAGE to a registered or
IP-authenticated trunk, and whether it accepts outbound MESSAGE for SMS, is
unverified. With a real trunk check: receive a text; send a reply; a long reply
(split); non-ASCII text; what the carrier does on STOP; what happens when the
trunk refuses (the reply shows failed).
