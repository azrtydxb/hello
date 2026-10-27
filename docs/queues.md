# Call queues

A call queue (ACD) answers a call, holds the caller with music and
announcements and offers the call to the next free agent. Queues are managed
under **Queues** in the console, or through `/api/v1/queues` (and the matching
MCP tools). The **Queue wallboard** shows them live.

## Building a queue

| Field               | Meaning                                                                                                                                        |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| Name                | Unique, 1-64 of `A-Z a-z 0-9 . _ -`. Other settings name a queue by it.                                                                        |
| Number              | Optional, 2-16 digits. Dialling it, or transferring a call to it, joins the queue. It must not be an extension, ring group name or IVR number. |
| Strategy            | How the agent is picked, see below.                                                                                                            |
| Ring an agent for   | Seconds an agent rings before the next one is tried (5-120, default 20).                                                                       |
| Retry after         | Seconds before a call nobody answered is offered again (0-120, default 5).                                                                     |
| Wrap-up time        | Seconds an agent is left alone after a call (0-600).                                                                                           |
| Callers at most     | 0 for no limit. A caller who finds the queue full goes to the **full** destination.                                                            |
| Wait at most        | Seconds (0 for no limit, default 600). Then the **timeout** destination applies.                                                               |
| Join with no agents | Whether a call may join while no agent is logged in. Off sends it to the **no agents** destination at once.                                    |
| Leave when empty    | Whether waiting callers leave (to the **no agents** destination) when the last agent logs out.                                                 |
| Music on hold       | A music on hold class (see [media.md](media.md)). Empty uses the global default class, else the built-in tune.                                 |
| Agents              | Extensions with a static/dynamic flag and a penalty. A static agent is always available, a dynamic one must log in.                            |

### Strategies

| Strategy       | Order                                                         |
| -------------- | ------------------------------------------------------------- |
| `ring_all`     | Every agent of a penalty tier at once.                        |
| `longest_idle` | The agent whose last call ended longest ago (never first).    |
| `round_robin`  | Starts one agent further on for each call.                    |
| `fewest_calls` | The agent who answered the fewest calls (ties: longest idle). |
| `random`       | A random order, fixed per call.                               |
| `linear`       | The order of the agent list.                                  |

Penalties apply under every strategy: all agents with the lowest penalty are
tried first, a higher penalty only once every lower one has been tried.

### Failover destinations

The queue sends a caller on when the maximum wait passes (**timeout**), the
queue is **full**, or **no agent** is logged in. Each uses the IVR destination
kinds: hang up, extension, ring group, voicemail, announcement, paging group,
IVR menu, external number, time condition and **another queue**. A queue whose
destinations lead back to itself through other queues is refused. A time
condition can in turn send its callers to a queue.

### Announcements

Every _announce every_ seconds the caller hears, in order: the periodic
announcement, optionally "you are caller number" and the position, optionally
"the estimated wait is", the minutes and "minutes", and the voicemail offer.
Numbers are spoken digit by digit from announcements named `<prefix>0` to
`<prefix>9` (the _digits prefix_); every prompt is an announcement, so
uploaded or text-to-speech generated prompts both work. A missing prompt is
skipped and traced.

The estimated wait is `(position - 1) x average call length / staffed agents`,
rounded up to whole minutes; the average is measured per queue (3 minutes
until it has one).

The **voicemail key** (for example `1`) can be pressed at any time while
waiting: the caller leaves a message in the box of the chosen extension and
leaves the queue.

## Agents

An agent is an extension in at least one queue. Agent state is global to the
extension and lives in Valkey, so every node and the console see the same.

| Code  | Action                                                                                                           |
| ----- | ---------------------------------------------------------------------------------------------------------------- |
| `*45` | Log in to every queue the extension is a dynamic agent of; `*45` plus a queue number logs in to that queue only. |
| `*46` | Log out (this also clears a pause).                                                                              |
| `*47` | Pause; the code's argument is the reason (default `break`).                                                      |
| `*48` | Unpause.                                                                                                         |

These are the default feature codes of a new installation and are seeded into
an existing one only when free. The API refuses a feature code that equals,
starts with or is started by another feature code or a paging group's page
code (the same rule that keeps `*80` and a page code apart), which covers the
park and pick-up codes of the park/pick-up feature as they are feature codes.

The same changes are made with `PUT /api/v1/queue-agents/{extension}`
(`loggedIn`, `paused`, `reason`, `queues`) and the buttons on the wallboard.

**BLF.** A phone can subscribe to `<login code><extension>` (for example
`*45101`) for a login lamp, or `<pause code><extension>` (`*47101`) for a pause
lamp; the lamp is lit while the agent is logged in, or paused. The lamps follow
changes made from anywhere within two seconds.

An agent is skipped when logged out, paused, in DND, wrapping up, busy in a
call, ringing for another queue call, or without a registered phone.

## Getting calls into a queue

- Dial the queue's number.
- An inbound route with destination **Call queue**.
- An IVR key, or the IVR's timeout or invalid exit.
- A ring group's failure destination **Call queue**.
- A time-condition or other destination picker that lists queues.
- A blind or attended transfer to the queue's number (REFER): the transferee's
  leg drops and the caller joins the queue as a call of its own. For an
  attended transfer the consultation call may itself wait in the queue; the
  transferee then takes its place.

## Media

A waiting caller is answered through Hello's media relay, like an IVR menu, so
it is codec-agnostic: the relay encodes the music and announcements in
whatever the caller's leg negotiated. Music is the queue's music on hold
class (else the global default, else the built-in tune), played by the same
player as a hold.
Announcements are fetched as announcement objects, the same as IVR prompts.

## Shared state and high availability

Queue state is in Valkey (`hello:queue:*`): the waiting callers with their
join time (the order is `(join time, call id)`, on Valkey's clock), the agents'
state, per-queue statistics, and leases.

- The node holding a caller's dialog offers the call. It takes a lease on the
  call and on each agent it rings, so two nodes never ring one agent for two
  callers, nor offer one call twice. A caller is offered only once every
  caller ahead of it is being served.
- An entry that is not refreshed (the node died) disappears after 20 s. A
  surviving node that takes the call over restores the caller with its
  original join time, so it stays at the same position. See
  [ha.md](ha.md): a queued call replicates as a one-dialog call and is
  re-INVITEd onto the survivor's relay.
- A call answered by an agent after a takeover is an ordinary taken-over
  two-leg call.
- What does not survive a node death: the statistics of a call that was
  already bridged (its agent's wrap-up time is not started).

## CDR, metrics and logs

The CDR of a queued call lists the queue legs in order: `enter`, `agent` (each
ring with `answered`, `no_answer` or `busy`), `wait` (with the time waited and
how it ended), `answered`, `abandon` and `exit` (with `timeout`, `full`,
`no_agents`, `breakout` or `callback`). A callback adds `callback_requested`,
`callback_dialled`, `callback_connected` and `callback_failed` (see
[Callback](#callback)). The call detail API returns them as `queueLegs`.

| Metric                                    | Meaning                                                                                     |
| ----------------------------------------- | ------------------------------------------------------------------------------------------- |
| `hello_queue_waiting{queue}`              | Callers waiting in queues this node holds; sum it across nodes.                             |
| `hello_queue_wait_seconds{queue,outcome}` | Histogram of the wait by outcome (answered, abandoned, timeout, full, no_agents, breakout). |
| `hello_queue_abandoned_total{queue}`      | Callers who hung up while waiting.                                                          |
| `hello_queue_calls_total{queue,event}`    | entered, answered, abandoned, `exit_<reason>`.                                              |
| `hello_queue_agents{queue,state}`         | Agents by state, reported by one node at a time (the holder of a lease).                    |
| `hello_callback_total{result}`            | Callback outcomes: requested, coalesced, full, dialled, connected, no_answer, busy, failed, abandoned, no_agent. |

## Callback

A queue can offer a caller a call back instead of holding the line. It is off
by default; turn it on in the queue's **Callback** section (or
`callbackEnabled`, `callbackOfferAfterSeconds`, `callbackDigit` through the API).

- **The offer.** Once the estimated wait exceeds the threshold (default 120 s;
  with nobody staffed, the time already waited stands in for the estimate; 0
  offers at once) the caller hears the offer once. The callback key (default
  `2`) accepts it, and pressed at any time while waiting it asks for a callback
  without the offer. The key must differ from the voicemail key. A caller with
  no dialable number (anonymous) is never offered a callback. The offer needs a
  text-to-speech voice (Settings, Voice).
- **The request.** Accepting plays a confirmation and ends the call. A
  `queue_callbacks` row records the caller's number, name, status `waiting`
  and `position_key`, the caller's original enqueue time. A second request from
  a number already waiting in the queue is coalesced into the first. A queue
  holds at most 100 open requests. A request expires after 2 hours; finished
  rows are kept 7 days.
- **The place is kept.** A callback sorts by `position_key` among the waiting
  callers. A caller who waited less is not offered an agent ahead of a due
  request, and the request is not dialled while a caller who waited longer is
  still unserved.
- **The call.** A sweep on every node claims the head request when a staffed
  agent is free (`FOR UPDATE SKIP LOCKED`, under a per-queue lease). The caller
  is rung first, as the queue (its number, else its name) through the caller's
  extension or the outbound route that matches, decided as a call from the
  agent the strategy would ring first. The agent is rung only after the caller
  answered. While the phone rings, the sweep holds the agent (its lease) so a
  caller who waited less cannot take it. An outside number is dialled through the
  route's trunks in route order, failing over on the route's failover codes as a
  normal outbound call does. The answered caller hears a welcome prompt and music, then joins the
  queue at its original place and is offered like any waiting caller.
- **Attempts.** No answer, busy, a failure, a hang-up before an agent was
  connected, or no agent within the queue's maximum wait counts as an attempt.
  The next is 2 minutes later, at most 3; then the request is `failed`. That is
  a `callback_failed` leg, not an abandon.
- **High availability.** Requests are in PostgreSQL. A claim carries a 60 s
  lease, renewed while the caller is on the line. A node lost before the caller
  answered leaves the row `dialling`; the lease lapses and another node claims
  it. A node lost while the caller waits on the line is a takeover like any
  queued call: the survivor re-INVITEs the caller, adopts the request and
  keeps the place. While PostgreSQL is unavailable, callbacks wait.
- **Language.** The prompts are spoken in the queue's **Prompt language**, else
  the deployment language, else English; a language whose clip cannot be
  produced is spoken in English.
- **Cancelling** a request that is being dialled stops it within about 2 s: a
  ringing phone is cancelled, a caller already on the line is hung up, and the
  request is not retried.
- **Operators.** `GET /api/v1/queues/{id}/callbacks` and `DELETE
  /api/v1/queues/{id}/callbacks/{callbackId}` (also the **Callbacks** page of a
  queue) list requests and cancel an open one.

The CDR of the call placed to the caller has kind `callback`.

## Wallboard

**Queue wallboard** (`/wallboard`, `GET /api/v1/queues/status`) refreshes every
5 seconds, like the other live pages. It shows each queue's waiting callers
(position, wait, who rings), the longest wait, and every agent with a state
(available, on a call, ringing, DND, wrapping up, paused, logged out) and
log-in/pause buttons for operators.

## Migration

Migration `00040_call_queues.sql` adds `queues`, `queue_members` and
`cdr_queue_legs`, widens the destination kind checks of inbound routes, ring
groups and IVR menus with `queue`, and the feature-code actions with the four
agent actions. It is additive, so a rolling upgrade works.
