# The call-flow canvas

The Call flows pages (Call flow, then Call flows) draw Hello's configuration as
a graph: trunks, inbound and outbound routes, time conditions, IVR menus, ring
groups, queues, extensions, voicemail, announcements, paging, park and the
rest, joined by the connections that carry a call. A canvas is a named, shared
layout of that graph ("Main office", "After hours"); it stores only where nodes
sit, never the configuration.

## Roles

| Role     | Can                                                                                                           |
| -------- | ------------------------------------------------------------------------------------------------------------- |
| viewer   | Look at every canvas, trace a call, read warnings, look at AI proposals drawn on the canvas.                  |
| operator | All of that, plus move nodes, draft changes and apply them, ask the AI, apply AI proposals, let the AI apply. |
| admin    | All of that, including admin-only objects (paging groups, conferences, multicast) in a change.                |

## Editing

Changes are drafted on the canvas (create a node from the palette, draw or
remove a connection, edit a node in the side panel), checked with a dry run
as you go, reviewed as a diff and applied in one transaction with **Apply**.
An applied change can be reverted from **History**. Under the hood this is one
configuration batch (`POST /api/v1/config/batch`, see
[config-batch.md](config-batch.md)).

## Tracing a call

**Trace a call** asks where a call would go: who calls (an extension or a
trunk), the number, optionally the caller ID, the time (schedules, holidays and
day/night overrides are read at that moment) and the keys pressed in menus. The
path is highlighted on the canvas and listed step by step. Nothing is dialled
and nothing is written, so viewers can trace too.

## Ask AI

**Ask AI** opens the assistant beside the canvas. It needs the AI agent to be
on (a model endpoint configured, see [ai-agent.md](ai-agent.md)); without it
the panel says why and links to the AI status page. Asking needs the operator
role.

- **Explain.** "Explain this canvas", or with a node selected "Explain from the
  selected node": the assistant reads the graph (`getFlowGraph`, usually the
  closure of one node) and describes where calls go in plain words.
- **Why did this call go here?** After a trace, the assistant traces the same
  call (`traceFlow`) and explains each step and the rule that chose it: the
  matched route, the schedule or override, the key pressed, the fallback.
- **Propose a change.** Describe it ("after 18:00 send the main number to the
  voicemail of 100"). The assistant answers with a proposal whose single
  action is a configuration batch, or, with **Let AI apply changes** on,
  applies that batch itself (below).

Every question carries the canvas as context (the canvas id, the selected
node and a traced call). The assistant receives it as data, not instructions,
and reads the configuration through its own read-only tools as you, with your
role.

### A proposal on the canvas

With **Let AI apply changes** off (the default) a proposal is never applied by
the assistant. When it is made, Hello checks
its batch the way an apply would: every operation must be an object write a
batch accepts, none may set a password, PIN or secret, there are at most 50,
and the whole batch is dry-run as you, with your role, and rolled back. A
batch that would fail is never stored; the assistant is told why and tries
again.

The proposal is then drawn over the canvas:

- new nodes and connections are outlined and placed next to what they connect
  to (they cannot be dragged until they exist);
- changed nodes are outlined;
- deleted nodes and dropped connections are struck out.

The panel lists the operations, the warnings the change introduces (an
unreachable menu, a loop) and the counts. **Apply** asks for confirmation and
applies the batch in one transaction with your credentials; the new nodes are
then placed on the canvas where they were drawn. **Discard** dismisses the
proposal as not needed. Open proposals can be shown again from the panel's
list, and a proposal page links to its canvas (`/flow/<canvasId>?proposal=<id>`).

If the configuration changed since the proposal was made, applying is refused
when any object the batch changes changed too (the proposal becomes stale and
nothing is sent); changes to other objects do not matter.

### Let AI apply changes

The panel's switch **Let AI apply changes** is off whenever the panel opens
(ask first). It holds for the questions you send while the panel stays open on
this canvas, and each question records whether it was on. With it on, the
assistant may apply the change you asked for itself, through its one write
tool (`applyConfigBatch`):

- the batch is checked exactly as a proposal's (same operations, no secrets,
  at most 50, dry-run as you) and stored as a proposal, so the panel and the
  proposal page show it;
- it is then applied in one transaction as you, with your role: a viewer can
  never apply (the panel offers no switch, and the API refuses the batch), and
  an operator cannot apply admin-only objects;
- the assistant can do nothing else with write access: no direct object
  write, layout or revert, only that one batch per question.

When it applied something, the panel shows **Applied by the assistant for
you** with the counts and the operations, marks the new and changed nodes on
the canvas, and offers **Revert** (dry run first, then one confirmation), the
same revert as **History**. A refused, stale or failed batch changes nothing;
the answer says why.

### What is logged

- The question, with its canvas context, and the answer are kept in the AI
  session (AI, then Assistant).
- The proposal, with the dry run it was checked with, is kept until it is
  applied or dismissed; applying and dismissing write an audit row with who did
  it.
- An applied proposal is one configuration batch whose origin (`via`) is
  `ai-proposal:<id>`, with an audit row per object it changed. It appears in the
  canvas **History** and can be reverted there.
- A batch the assistant applied itself names you as the actor and has the
  origin `ai:<session>`: the batch row, the proposal (`appliedVia`, `batchId`)
  and the proposal's audit row carry it, and each object it changed has its own
  audit row. **History** and the proposal page show it as applied by the
  assistant for you, and it reverts like any other batch.

## For MCP clients

External agents use the same API through MCP: `getFlowGraph`, `traceFlow` and
the canvases with scope `read`, and `applyConfigBatch` (dry run first) with
scope `write`. The skill `hello-flow` describes the workflow.
