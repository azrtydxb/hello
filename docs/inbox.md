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

- a website chat widget for intranet pages and VPN users, served on the LAN only
  (see "Website chat" below).

Not yet: SMS through a carrier's web API (Twilio, Telnyx), WhatsApp, texts on
desk phones, queues and automatic assignment, and AI. None of the providers
that need an Internet-facing address is possible while Hello is reachable only
on the LAN, and neither is a widget on a public website.

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
| `/api/v1/messaging/addresses` (+ `/{id}`)                                                 | admin, `admin`                |
| `/api/v1/messaging/settings`                                                              | admin, `admin`                |
| `/api/v1/messaging/optouts` (+ `/{id}/reconsent`)                                         | admin, `admin`                |
| `POST /api/v1/inbox/erase`                                                                | admin, `admin`                |
| `/api/v1/messaging/chat/sites` (+ `/{id}`, `/{id}/blocks`), `/chat/blocks/{id}`           | admin, `admin`                |
| `/api/v1/inbox/conversations/{id}/block`, `/email-reply`, `/typing` (and `/me/inbox/...`) | operator or agent, `messages` |
| `/api/v1/inbox/conversations...`, `/api/v1/inbox/stats`                                   | operator, `messages`          |
| `/api/v1/me/inbox/conversations...`, `/status`, `/agents`, `/stream`                      | any agent, `messages`         |

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
