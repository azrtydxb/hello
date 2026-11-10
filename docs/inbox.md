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
- Hello's own opt-out handling, retention and erasing a customer.

Not yet: SMS through a carrier's web API (Twilio, Telnyx), WhatsApp, the chat
widget, texts on desk phones, and AI. None of
the providers that need an Internet-facing address is possible while Hello is
reachable only on the LAN.

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

## API

| Route                                                                | Role and scope        |
| -------------------------------------------------------------------- | --------------------- |
| `/api/v1/messaging/channels` (+ `/{id}`, `/{id}/test`)               | admin, `admin`        |
| `/api/v1/messaging/addresses` (+ `/{id}`, `/{id}/routing`)           | admin, `admin`        |
| `/api/v1/messaging/settings`                                         | admin, `admin`        |
| `/api/v1/messaging/optouts` (+ `/{id}/reconsent`)                    | admin, `admin`        |
| `POST /api/v1/inbox/erase`                                           | admin, `admin`        |
| `/api/v1/inbox/conversations...`, `/stats`, `/queues`                | operator, `messages`  |
| `/api/v1/me/inbox/conversations...`, `/status`, `/agents`, `/stream` | any agent, `messages` |
| `/api/v1/me/inbox/agent` (GET, POST: log in, pause)                  | any agent, `messages` |

Queues take `textEnabled`, `textCapacity`, `textReassignSeconds` and
`textRequiresIdleVoice`.

Conversations list with `view` (`all`, `mine`, `unassigned`), `status`, `q`
(number, name or text), `channelId`, `queueId`, and a cursor. Every operation is in
[the OpenAPI document](../internal/api/openapi.json).

## Metrics

`hello_msg_inbound_total{provider,result}` (stored, duplicate, not_text,
failed), `hello_msg_optout_total{source}`, `hello_msg_purged_total{kind}`,
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
