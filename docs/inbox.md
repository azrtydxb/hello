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
- routing: a number sends its conversations to a queue, a user, an extension's
  user, a ring group's users or the pool, with opening hours, and a text queue
  hands them to its agents by the queue's strategy (see
  [Routing and queues](#routing-and-queues));
- Hello's own opt-out handling, retention and erasing a customer;
- sending SMS through a **Twilio** account (see "Sending through Twilio"):
  outbound only;
- a website chat widget for intranet pages and VPN users, served on the LAN only
  (see "Website chat" below).

Not yet: receiving texts from a carrier's web API (needs an Internet-facing
address), Telnyx, WhatsApp, and AI. None of the providers that need an
Internet-facing address is possible while Hello is reachable only on the LAN,
and neither is a widget on a public website.

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

New conversations arrive **unassigned** unless the number routes them
elsewhere (below): every operator and administrator sees them in the pool. Anyone who may see one can **take** it (claim). A claim is a
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

## Routing and queues

**Where a number's new conversations go** is set per number (Messaging >
Channels > Routing, or `PUT /api/v1/messaging/addresses/{id}/routing`):

| Route        | What happens                                                                                               |
| ------------ | ---------------------------------------------------------------------------------------------------------- |
| Pool (inbox) | Unassigned; every operator and administrator sees it. The default.                                         |
| Queue        | The queue's strategy picks an agent (below).                                                               |
| User         | Assigned to that user.                                                                                     |
| Extension    | Assigned to the user the extension is linked to.                                                           |
| Ring group   | A shared pool of the users of the group's extensions: it stays unassigned and the first to reply takes it. |

A target that is deleted, disabled or cannot take conversations (a queue that
stopped taking texts, an extension nobody is linked to) sends the conversation
to the pool instead; nothing is lost. Routing is by the number, not the channel:
every channel that merges later is routed the same way. A conversation is placed
once, when it starts; a reopened one keeps where it was.

**First contact.** A number may have a first-contact text, sent once to a
customer who texts it for the first time (never again to the same customer on
that number, never to one who opted out, and not in addition to the
closed-hours reply). Like the closed-hours reply it is sent by a SIP trunk
channel only today.

**Opening hours.** A number may have a weekly schedule (a time zone and
windows). A conversation that starts outside it gets the **after-hours action**:
route as usual (the default), send a reply text and then route as usual, or go
to another target. The reply is sent once, never to a customer who opted out,
and only a SIP trunk channel can send it today (another channel's conversation
gets a line in the thread saying it was not sent). The time-conditions engine
is not used for texts; the number's own schedule is.

**Text queues.** On a queue (Queues > the queue), **Texts** turns it on and sets
how many open conversations each agent holds (default 3, 1 to 50), after how
many seconds an unanswered conversation returns (default 300, 0 never) and
whether an agent on a call is offered none. Existing call queues are unchanged.
The queue's members are its agents: a member whose extension is linked to a
user. An agent is offered a conversation when they are **staffed** (a static
member always; a dynamic member when logged in), not paused, under their limit
and, if the queue says so, not on a call. This is the call queues' own login
and pause state, in Valkey: an agent logs in and pauses in the inbox (or with
the feature codes), and it counts for calls and texts alike. The strategies
are `queue.Plan`'s (`longest_idle`, `fewest_calls`, `round_robin`, `random`,
`linear`, with penalties), fed with the number of text conversations the queue
gave each agent and when; `ring_all` means a shared pool, as above.

A conversation nobody can take waits in the queue, oldest first, and is given
to an agent as one becomes free (logs in, resumes, resolves one). One an agent
leaves unanswered for the queue's time returns to the queue and is offered to
the next agent, not the one who let it lapse; if nobody else can take it, it
waits and the members can still claim it. A conversation a person assigned or
took is never taken back. If an agent's extension is unlinked or they leave the
queue, their conversations return to the queue. Turning a queue's texting off
sends its unassigned conversations to the pool; a queue that a number routes to
or that holds an open conversation cannot be deleted (`409`).

Who sees what: a queue's members (and a ring group's users) see the unassigned
conversations waiting in it and can claim them; nobody else sees them. Operators
and administrators open **Messaging > Text queues** (`GET /api/v1/inbox/queues`)
to see each queue's waiting list (position and waiting time, no text or number),
its agents with their state and open conversations.

The router runs on every control replica every 2 seconds. It claims rows with
`SKIP LOCKED`, assigns by compare-and-set while counting the agent's open
conversations under a per-agent advisory lock, and keeps nothing in memory, so
two pods never assign a conversation twice or give an agent more than the
limit. If Valkey cannot be read it assigns to static members only.

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

## Website chat

A chat site is a widget an administrator embeds on **intranet pages** (or pages
reached over a VPN). Visitors are anonymous; their chat arrives in the inbox as
a conversation, and agents answer it there. It is **LAN only**: the public
widget needs the public listener, which is not built.

### Turning it on

1. Set `HELLO_MSG_CHAT_LAN_ADDR` (for example `:8087`) on hello-control. Unset,
   nothing listens and there is no extra port. Helm: `control.chat.lanAddr`.
   `HELLO_MSG_CHAT_ENABLED=false` is the global kill switch (every `/chat`
   request answers 503); `HELLO_MSG_CHAT_TRUSTED_PROXIES` lists the proxies whose
   `X-Forwarded-For` is believed (the UI pods and the ingress controller; without
   them every visitor shares one address and one set of limits);
   `HELLO_MSG_CHAT_MAX_INFLIGHT` (500) and `HELLO_MSG_CHAT_MAX_STREAMS` (2000) bound
   a pod.
2. The listener has its own `http.Server` and serves only `/chat/*` and
   `/healthz`; the admin API, the UI, OAuth and MCP are not routed on it. The
   UI container proxies `/chat/` to it (`HELLO_CHAT_UPSTREAM`), so the widget is
   on the same LAN host as the console; the Helm chart sets this when the
   listener is on.
3. **Messaging, Chat sites**: add a site, list the intranet origins that may
   embed it (`scheme://host[:port]`), and copy the embed code:

   ```html
   <script
     async
     src="https://hello.lan/chat/v1/widget.js"
     data-site="SITE_KEY"
   ></script>
   ```

   The loader injects a sandboxed iframe on Hello's own origin, so the page's
   scripts and styles can neither read nor break the chat. The frame is served
   with `frame-ancestors` set to the site's allowed origins (`'none'` until
   some are listed) and `default-src 'none'; script-src 'self'; style-src
'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none';
form-action 'none'`. A Turnstile site also allows Cloudflare's host for
   scripts and frames, which its widget needs.

### What a visitor sees

A launcher bubble, an optional pre-chat form (name and email: off, optional or
required; the retention period and a privacy policy link are shown, and
consent is recorded), the conversation, a typing indicator and "Support"
as the agents' name. Outside the site's opening hours, with no supervisor
holding the inbox open, or with the live queue full, the widget offers to leave
a message and an email address (or hides itself, per site). A visitor may end
the chat and, when the site allows it, email themselves a transcript (three a
day). Messages are plain text everywhere; a link in a visitor's message is
clickable only in the inbox, with `rel="noopener noreferrer nofollow"` and one
confirmation per host before it leaves Hello.

### What an agent does

The conversation is an ordinary inbox conversation (peer kind visitor) with the
visitor's name, email, the page the chat started on and a coarse browser
family. An agent can **block the visitor** (24 hours by default, 30 days at
most; the day's network address too), **answer an offline chat by email** (the
reply and no link are emailed to the address the visitor gave and kept in the
conversation; the visitor sees it in the widget when they return; needs SMTP),
and the visitor sees when an agent is typing and when their messages were read.

### Abuse controls

All of them are server-side and none trusts the `Origin` header (anyone can run
`curl`):

- **Challenge** at session creation: a stateless proof of work (default, 14 to
  22 bits, no third party), Cloudflare Turnstile with the customer's own keys, or
  none (the page warns). A solved nonce works once, only for the address it was
  issued to, and expires in three minutes. The difficulty rises one level for 15
  minutes while a site gets more than three times its usual session rate
  (`hello_chat_challenge_raised_total`).
- **Limits** (Valkey, with the same windows per pod in memory when Valkey does
  not answer; never open): sessions 5 a minute and 30 an hour per address and 200
  a minute per site; messages 20 a minute and 300 a day per visitor and 10,000 a
  day per site; typing, history, event streams and transcripts have their own.
  Over a limit the answer is `429` with `Retry-After` and no body.
- **Caps**: 2000 characters a message (valid UTF-8, control and invisible
  formatting characters removed, normalised), 16 KiB a request, one open
  conversation per visitor, 200 messages before any agent reply, and the live
  queue cap per site. A pod answers 503 beyond 500 requests in flight; each
  request has a 10 second deadline; a pod holds at most 2000 event streams.
- **Blocklist**: a blocked visitor gets a normal-looking `200` with a closed
  state and reaches nobody. Addresses are only ever kept as an HMAC under a salt
  that changes at 00:00 UTC.
- **Content**: honeypot field, optional link stripping, no cookies, no
  fingerprinting, the user agent reduced to a browser family, the page to host
  and path.
- **Not built**: attachments and the site logo (they need the media pipeline,
  which is not on main).

Visitors' names, emails, tokens, addresses and text never reach a log, a metric
label or an audit row. Retention for finished chats is 90 days by default (a
site can set its own); visitors with no chat left are removed after 30 days;
erasing `visitor:<id>` removes the visitor's whole record.

Metrics: `hello_chat_sessions_total{result}`, `hello_chat_messages_total{result}`,
`hello_chat_rate_limited_total{rule}`, `hello_chat_challenge_raised_total`,
`hello_chat_rejected_total{reason}` and `hello_chat_streams`.

## API

| Route                                                                                     | Role and scope                |
| ----------------------------------------------------------------------------------------- | ----------------------------- |
| `/api/v1/messaging/channels` (+ `/{id}`, `/{id}/test`)                                    | admin, `admin`                |
| `/api/v1/messaging/addresses` (+ `/{id}`, `/{id}/routing`)                                | admin, `admin`                |
| `/api/v1/messaging/settings`                                                              | admin, `admin`                |
| `/api/v1/messaging/optouts` (+ `/{id}/reconsent`)                                         | admin, `admin`                |
| `POST /api/v1/inbox/erase`                                                                | admin, `admin`                |
| `/api/v1/messaging/chat/sites` (+ `/{id}`, `/{id}/blocks`), `/chat/blocks/{id}`           | admin, `admin`                |
| `/api/v1/inbox/conversations/{id}/block`, `/email-reply`, `/typing` (and `/me/inbox/...`) | operator or agent, `messages` |
| `/api/v1/inbox/conversations...`, `/api/v1/inbox/stats`, `/api/v1/inbox/queues`           | operator, `messages`          |
| `/api/v1/me/inbox/conversations...`, `/status`, `/agents`, `/stream`                      | any agent, `messages`         |
| `/api/v1/me/inbox/agent` (GET, POST: log in, pause)                                       | any agent, `messages`         |
| `POST .../conversations/{id}/contact` (add to contacts)                                   | as the area it is in          |
| `GET .../conversations/{id}/calls` (the number's recent calls)                            | as the area it is in          |

Queues take `textEnabled`, `textCapacity`, `textReassignSeconds` and
`textRequiresIdleVoice`.

Conversations list with `view` (`all`, `mine`, `unassigned`), `status`, `q`
(number, name or text), `channelId`, `queueId`, and a cursor. Every operation is in
[the OpenAPI document](../internal/api/openapi.json).

## Metrics

`hello_msg_inbound_total{provider,result}` (stored, duplicate, not_text,
failed), `hello_msg_device_replies_total{result}` (sent, refused, failed: texts typed on
a desk phone), `hello_msg_optout_total{source}`, `hello_msg_purged_total{kind}`,
`hello_msg_unassigned` and `hello_msg_unassigned_oldest_seconds`, and for
routing `hello_msg_assign_total{result}` (placed, assigned, full, lost,
returned, after_hours_reply), `hello_msg_first_response_seconds`,
`hello_msg_queue_text_waiting{queue}` and `hello_msg_assign_live_down_total`.
No label carries a number, a name or text.

## HA

Every control and SIP replica can run every part: an inbound text is ingested in
one transaction (an event ledger makes a delivery seen once, on any pod; one open
conversation per number and customer is made under a unique index), claims and
assignments are compare-and-set, retention and notification claims use
`SKIP LOCKED` or a Valkey lease. Nothing is held only in memory, so a pod
restart loses nothing. Valkey is not needed to receive and answer texts; it
holds the agents' login and pause, and without it assignment falls back to
static queue members.

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
