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

- sending SMS through a **Twilio** or **Telnyx** account (see "Sending through
  Twilio", "Sending through Telnyx"): outbound only;
- reading the state of your US **sender registration** (A2P 10DLC brand and
  campaign, toll-free verification) from the carrier (see "Sender
  registration").

Not yet: receiving texts from a carrier's web API (needs an Internet-facing
address), WhatsApp, the chat widget, texts on desk phones, queues and
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
   Hello reads where it stands: see "Sender registration".

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

## Sending through Telnyx

A **Telnyx** channel works exactly like a Twilio one (the same first-contact
rules, queue, at-most-once send, status polling, opt-out and failure states,
all described above); only the account differs. Telnyx numbers **cannot
receive** texts yet, for the same reason: Telnyx delivers an inbound text by
calling a public webhook.

1. **Messaging > Channels > Add channel**, carrier **Telnyx**: a Telnyx v2 API
   key (from the Telnyx portal) and optionally a **messaging profile ID** (a
   UUID) when texts should leave through that profile's sender pool and
   settings. Hello lists the account's messaging numbers with Telnyx before it
   saves anything (a refused key is `422`, an unreachable Telnyx `502`), then
   seals the key with `HELLO_SECRET_KEY`. **The key is never shown again.** To
   change it, enter the key and the profile together; they replace the stored
   ones after Telnyx accepts them. **Test** and
   `GET /api/v1/messaging/channels/{id}/numbers` work as for Twilio.
2. **Add number**: the Telnyx number in international format.
3. Register the sender in the Telnyx portal as for Twilio (below).

Differences worth knowing:

- Status is read with `GET /v2/messages/{id}`, which Telnyx answers for ten
  days after the send. Hello polls for 48 hours, so the limit never applies.
- A carrier that cannot confirm delivery reports `delivery_unconfirmed`; Hello
  shows it as **sent** and keeps asking until 48 hours have passed.
- Telnyx blocks a recipient who replied STOP and fails the send with error
  `40300`. Hello records it as an opt-out with source "carrier", like Twilio's 21610. This code was taken from Telnyx's documentation and has not been
  checked on a live account.
- A Telnyx `403` that carries a documented error code is that error, not
  refused credentials; a `403` without one is a refused key.

## Sender registration

Texting US numbers from a long code needs an **A2P 10DLC** brand and campaign
registered with your carrier, and a toll-free number needs **toll-free
verification**; carriers filter or surcharge texts from an unregistered
sender. Hello does not register anything (that is done in the Twilio or Telnyx
console, with your business details). It **reads** where each number stands, so
you can see what is left before go-live.

**Messaging > Channels**: the numbers table has a **Registration** column for
numbers on a Twilio or Telnyx channel, and **Registration of &lt;channel&gt;**
opens the full picture: a checklist, the account's brands, its campaigns and
every number of the account (also those not added to Hello) with its state.
`GET /api/v1/messaging/channels/{id}/registration` returns the same (admin
only; nothing is stored, each call reads the carrier).

- A number's **kind** says which registration it needs: `10dlc` for a US
  long code (+1 and not toll-free), `toll_free` for a North American toll-free
  number (800, 833, 844, 855, 866, 877, 888), `other` for the rest of the
  world. Hello reads no registration outside the US and Canada: those numbers
  are **unknown** (country rules such as UK or EU sender IDs and India DLT are
  not read at all).
- A number's **state** is `approved`, `pending`, `rejected`, `unregistered` or
  `unknown`. A long code is approved when it sits on an approved campaign
  (Twilio: in a Messaging Service with a verified campaign; Telnyx: assigned
  to an active campaign); a toll-free number is approved when its
  verification is.
- If the carrier will not answer part of it (an API key without the 10DLC
  permission, say), that part is listed under `unavailable` and the numbers
  that depend on it are **unknown**, not unregistered. If it answers nothing,
  the route is `422` (refused) or `502` (unreachable).
- The **checklist** has six steps: brand, campaign, numbers on a campaign,
  toll-free verification, sample messages, and an answer to HELP. A step that
  does not apply to the account's numbers is "not needed". Hello cannot read
  the sample messages a carrier reviews, so that step is always "check
  yourself": carriers reject a campaign whose samples lack opt-out language
  such as "Reply STOP to opt out". The HELP step is Hello's own setting: it is
  done when every enabled number has an answer to HELP. Each step links to the
  carrier's documentation.

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

## Contacts and names

A customer's number is named for whoever reads it, at read time, from the same
books as caller-name lookup: the extensions, **their own personal contacts**,
the named books of their phones' groups, then the company book. A personal
contact names the number for its owner only and is never stored on the
conversation; a name from a shared book (extensions, company) is stored with
its source, so everyone sees it. If no book names the number, the name the
carrier sent is shown. **Add to contacts** in the conversation's details saves
the number under a name: to your own contacts (a user an administrator has not
linked to an extension has none), or, for an operator or administrator, to the
company book.

The details also show a **Call** button and the number's **recent calls**. The
button opens the Dial page (click-to-call): Hello rings your own phone first and
dials the customer when you answer. The list holds the latest calls to or from
the number; a supervisor sees every such call, an agent only the calls their own
extension took part in, as in their own call history. A chat visitor has neither.
The numbers match by their last nine digits, so `0475 12 34 56` and
`+32 475 12 34 56` are one customer.

## Desk phones

A text can follow the conversation to the desk phone. Two switches must both be
on, both off by default: **Deliver to desk phones** on the number (Messaging
channels) and **Show customer texts on my desk phone** in the user's profile.
Then, when a conversation is assigned to or taken by that user, and for each
later customer text while it is theirs, Hello queues a SIP MESSAGE to every
registered phone of the user's extension (an unregistered phone gets it at its
next REGISTER, within the text queue's TTL). The phone shows it from the
customer, with the contact name resolved for that user, and a non-SMS channel
is prefixed (`[WhatsApp] `). A text longer than 700 bytes is split into parts
numbered `(1/3) `. Turning text messaging off ([messaging.md](messaging.md))
stops phone delivery but never the inbox.

A text typed on the phone to the customer's number replies into the
conversation, in this order: the user's own open conversation with that
customer; else an existing conversation with them on a number whose **owner
extension** is the phone's extension (unassigned or the user's). It is sent as a
reply from the web would be: the sender is the phone's authenticated extension
(never the `From` header), an opted-out customer is refused, and it is limited
to the text queue's per-minute rate. A refusal is answered to the phone as a
text from the system with the reason (`403`, or `429` for the rate). A customer
who is in another agent's open conversation is refused too, not texted around
the inbox. A number with no conversation is not the inbox's and takes the
ordinary external-number path unchanged: **a phone never makes first contact
through the inbox**. Only text is accepted from a phone.

Only Grandstream is known to show SIP MESSAGE on the handset (P22650 in the
built-in template); other vendors are off until a lab check. A user whose phones
are all of such vendors does not see the profile switch.

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
| `POST .../conversations/{id}/contact` (add to contacts)              | as the area it is in  |
| `GET .../conversations/{id}/calls` (the number's recent calls)       | as the area it is in  |

Conversations list with `view` (`all`, `mine`, `unassigned`), `status`, `q`
(number, name or text), `channelId`, and a cursor. Every operation is in
[the OpenAPI document](../internal/api/openapi.json).

## Metrics

`hello_msg_inbound_total{provider,result}` (stored, duplicate, not_text,
failed), `hello_msg_device_replies_total{result}` (sent, refused, failed: texts typed on
a desk phone), `hello_msg_optout_total{source}`, `hello_msg_purged_total{kind}`,
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

With real Grandstream phones (WP822 and WP836, IM account `P22650 = 1`):
assign a conversation to a user with both switches on and see the text on the
phone; a long text arrives as numbered parts; reply from the phone and see it
in the conversation and at the customer; a reply to an opted-out customer is
refused with the reason shown on the phone; the contact name shows as the
sender.
