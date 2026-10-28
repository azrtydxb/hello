# flow-canvas

Status: approved (2026-10-25). P0, P1, P2 and P3 delivered, and the graph side of P7 (every merged object type is in the catalogue and the graph), 2026-10-26. The **[assumed]** shapes were checked against the merged code and are now real; where they differed the text below says so.

Source: user request 2026-10-25 ("a special configuration canvas as an alternative to some of the forms … connect trunks, extensions, groups, IVRs … right panel … connector dots … colored … our AI agent should be able to use our canvas, build it, change it, answer questions on it"). Builds on `ai-external-access` (OpenAPI → MCP tools, roles, scopes, in-process replay), `ai-agent` (suggest-only proposals, S-10..S-14), `trunks-routing`, `pbx-features`, and the features that have since merged: IVR (`migrations/00033_ivr.sql`), time conditions (`00038`, `00039`), music on hold (`00029`), call queues (`00040`), conferences (`00034`), park and pickup (`00032`), multicast channels (`00016`, `00031`) and TTS prompts (`00017`).

Facts this design rests on (verified in the tree on 2026-10-25):

- Every configuration write goes through `store.configChangeID` (`internal/store/store.go`): one transaction, advisory lock `configLockKey`, the whole-config routing `Check` compared against a baseline, an audit row, a `schema_info.config_revision` bump and `pg_notify` to hello-sip. There is no per-object version or ETag; the global revision is the only concurrency token (as `ai/proposal/apply.go` uses).
- Destinations are `(kind, target)` strings, not foreign keys: `inbound_routes.destination_kind/destination` (`extension|external|sip_uri|internal`, plus `ivr` on branch `ivr`), `ring_groups.failure_kind/failure_target` (`none|voicemail|external|announcement`, plus `ivr`), `extensions.forward_always/busy/no_answer` (numbers), `feature_codes.action/argument`, `ivr_menu_options.dest_kind/dest_target` and the IVR `timeout_*`/`invalid_*` exits (`hangup|extension|ring_group|voicemail|announcement|paging|ivr|external`). Ring groups are dialled by name (`snapshot.RingGroup(number)`, checked before extensions in `sip.ringTarget`).
- Foreign keys: `devices.extension_id`, `phones.device_id`, `ring_group_members`, `paging_group_members/callers`, `outbound_route_trunks`, `inbound_routes.trunk_id` (NULL = any trunk), `ivr_menus.greeting_id/invalid_prompt_id/timeout_prompt_id → announcements`.
- MCP tools are generated one per OpenAPI operation (`internal/mcp/tools.go`); the assistant's read tools are an explicit list in `internal/ai/assistant/tools.go`; proposals carry ≤ 8 `{operationId, pathParams, body}` actions from `proposal.Allowlist`.
- `web/` depends only on react 19, react-dom and react-router 8; forms live as private `XxxDrawer` components inside each page and save directly; the design system has `Drawer` (with `inline`), `ConfirmDialog`, `Badge`, `Alert`.
- Everything the spec once marked **[assumed]** is on main and was checked on 2026-10-26. Differences from the assumed shapes: a time condition has no `holidays` input (a calendar attaches to a _schedule_, through `schedule_calendars`, and the condition's `holiday` branch is driven by the calendars of its schedule); queues, conferences, park lots, pickup groups and multicast channels are real node types; IVR/time-condition/queue destinations also accept `queue` and `time_condition`, and the IVR option kinds are stored as `hangup|extension|ring_group|voicemail|announcement|paging|ivr|external|time_condition|queue`.

## Problem

Hello's configuration is a set of forms, one object at a time. The call flow of a real PBX (DID → time condition → IVR → ring group → voicemail) is spread over five pages and joined by name strings, so nobody can see it, broken links are found only when a call fails, and building a multi-level IVR means jumping between pages in the right order. The AI agent can read each object but has no model of the flow, so "what happens when someone calls DID Y at 20:00 on Sunday" requires it to re-derive the graph by itself.

## Users

- **Operators:** build and change call flows by dropping objects, drawing connections and configuring in a side panel; preview the diff and apply it in one transaction; undo.
- **Viewers:** see the flows, trace a call, read validation warnings; cannot edit.
- **Administrators:** everything operators do, including admin-only objects (paging group create) inside a batch.
- **The in-product AI assistant:** reads the graph, traces calls, explains flows, and proposes changes that show as an overlay on the canvas for a human to apply.
- **External MCP clients:** read the graph and trace; with `write` scope, dry-run and apply batches.

## In scope

### Graph model

- [S-1] **Node catalogue.** One Go table (`internal/flow/catalogue.go`), published by `GET /api/v1/flow/catalogue`, defines every node type, its ports (id, direction, port type, cardinality, label, the stored field it maps to) and which input each output may connect to. The UI derives `isValidConnection`, port colours and the palette from it; the batch validator uses the same table.

  | Node (`kind`)                                                                                           | Inputs                                                                                                                                                                                                 | Outputs                                                                                                                                                                                                                                                                                             |
  | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
  | Trunk (`trunk`)                                                                                         | `carrier` (carrier, many, from outbound routes)                                                                                                                                                        | `inbound` (carrier, many → inbound routes)                                                                                                                                                                                                                                                          |
  | Any trunk (`anyTrunk`, pseudo)                                                                          | –                                                                                                                                                                                                      | `inbound` (carrier → inbound routes with `trunkId` null)                                                                                                                                                                                                                                            |
  | Inbound route / DID (`inboundRoute`)                                                                    | `trunk` (carrier, 0..1), `holdMusic` (0..1)                                                                                                                                                            | `destination` (call, 1): every call target                                                                                                                                                                                                                                                          |
  | Internal callers (`dialPlan`, pseudo)                                                                   | –                                                                                                                                                                                                      | `dial` (carrier → outbound routes)                                                                                                                                                                                                                                                                  |
  | Outbound route (`outboundRoute`)                                                                        | `dial` (carrier, 1)                                                                                                                                                                                    | `trunks` (carrier, ordered, many; edge label = try order)                                                                                                                                                                                                                                           |
  | Extension (`extension`)                                                                                 | `ring` (call, many), `voicemail` (voicemail, many; disabled when `voicemailEnabled` is false), `devices` (device, many), `holdMusic` (0..1)                                                            | `forwardAlways`, `forwardBusy`, `forwardNoAnswer` (call, 0..1 each: extension, numeric ring group, external number), `member` (member, many → ring group, paging group members/callers, queue, pickup group); implicit dotted `noAnswer → own voicemail` when no forward is set and voicemail is on |
  | Device (`device`), Phone (`phone`)                                                                      | phone: – ; device: `phone` (device, 0..1)                                                                                                                                                              | device: `extension` (device, 1); phone: `device` (device, 0..1)                                                                                                                                                                                                                                     |
  | Ring group (`ringGroup`)                                                                                | `ring` (call, many), `members` (member, ordered, many; position/weight/delay on the edge), `holdMusic` (0..1)                                                                                          | `noAnswer` (call, 0..1; unconnected = hang up): extension, voicemail, ring group, IVR, queue, announcement, time condition, external. Answered calls end in the group: no port.                                                                                                                     |
  | IVR menu (`ivr`)                                                                                        | `call` (call, many), `greeting` (prompt, 1, required), `invalidPrompt`, `timeoutPrompt` (prompt, 0..1)                                                                                                 | one `key:<digit>` port per configured key `0-9 * #` (call), plus a ghost "add key" port; `timeout`, `invalid` (call); `directDial` shown as a badge, not a port                                                                                                                                     |
  | Call queue (`queue`)                                                                                    | `call` (call, many), `members` (member, many; position, penalty and dynamic on the edge), `holdMusic`, `periodicPrompt`, `positionPrompt`, `waitPrompt`, `minutesPrompt`, `breakoutPrompt` (0..1 each) | `timeout`, `full`, `noAgents` (call, 0..1), `breakout` (voicemail, 0..1: the extension whose box takes the breakout message)                                                                                                                                                                        |
  | Announcement (`announcement`, TTS-generated ones badged `generated`, with `textEdited` and the voice)   | `call` (call, many: play, then hang up)                                                                                                                                                                | `audio` (prompt, many → IVR prompts, queue prompts, MOH class `tracks`, conference entry/exit announcements)                                                                                                                                                                                        |
  | Paging group (`pagingGroup`)                                                                            | `call` (call, many), `members` (member), `callers` (member)                                                                                                                                            | `multicast` (call, 0..1 → a multicast channel the page is also sent to)                                                                                                                                                                                                                             |
  | Multicast channel (`multicastChannel`)                                                                  | `page` (call, many)                                                                                                                                                                                    | –                                                                                                                                                                                                                                                                                                   |
  | Conference room (`conference`)                                                                          | `call` (call, many), `entryAnnouncement`, `exitAnnouncement` (prompt, 0..1)                                                                                                                            | – (the call stays in the room)                                                                                                                                                                                                                                                                      |
  | Park lot (`parkLot`)                                                                                    | –                                                                                                                                                                                                      | – (parking and retrieving are feature codes and transfers to a slot, chosen at call time)                                                                                                                                                                                                           |
  | Pickup group (`pickupGroup`)                                                                            | `members` (member, many)                                                                                                                                                                               | –                                                                                                                                                                                                                                                                                                   |
  | Feature code (`featureCode`, only actions with a target: `announcement`, `ivr`, `time_condition`)       | –                                                                                                                                                                                                      | `target` (call, 1)                                                                                                                                                                                                                                                                                  |
  | Time condition (`timeCondition`)                                                                        | `call` (call, many), `schedule` (schedule, 1)                                                                                                                                                          | `open`, `closed`, `holiday` (call, 0..1 each; with no `holiday` branch the closed destination serves holidays); day/night override shown as a badge (`forced`, `override`, `overrideAt`) with a toggle in the panel                                                                                 |
  | Schedule (`schedule`)                                                                                   | `calendars` (schedule, many)                                                                                                                                                                           | `use` (schedule → time condition `schedule`)                                                                                                                                                                                                                                                        |
  | Holiday calendar (`holidayCalendar`)                                                                    | –                                                                                                                                                                                                      | `use` (schedule → schedule `calendars`)                                                                                                                                                                                                                                                             |
  | MOH class (`mohClass`)                                                                                  | `tracks` (prompt, ordered, many; position on the edge)                                                                                                                                                 | `use` (hold music, many → `holdMusic` of extension, ring group, inbound route, queue)                                                                                                                                                                                                               |
  | External number (`external`), SIP URI (`sipUri`), DID-as-extension (`internalDid`), Missing (`missing`) | `call`                                                                                                                                                                                                 | – (terminal; created from the target string; `missing` is a red node for a target that resolves to nothing)                                                                                                                                                                                         |

  Every "call goes to..." output stores a `(kind, target)` pair; `StoredKinds` in `internal/flow/kinds.go` maps each kind to the input it reaches and `TestCatalogueDrift` keeps it in step with the database and the API.

- [S-2] **Port types and colours.** Each port type has a colour and a shape, so the canvas stays readable for colour-blind users. The colours are CSS variables `--flow-port-<type>` mapped to azrty tokens.

  | Type      | Meaning                                              | Colour           | Shape             |
  | --------- | ---------------------------------------------------- | ---------------- | ----------------- |
  | call      | a live call handed on                                | blue `#2563EB`   | circle            |
  | voicemail | leave a message in a box                             | violet `#7C3AED` | circle with notch |
  | carrier   | trunk-side routing (DID entry, outbound trunk order) | orange `#EA580C` | square            |
  | member    | an extension belongs to a group                      | green `#16A34A`  | diamond           |
  | prompt    | an audio prompt used by a node                       | amber `#CA8A04`  | triangle          |
  | schedule  | a schedule/holiday calendar used by a node           | teal `#0D9488`   | hexagon           |
  | holdMusic | an MOH class used by a node                          | pink `#DB2777`   | triangle          |
  | device    | device/phone binding                                 | slate `#64748B`  | small square      |

  Red is never a port colour: it marks validation errors only. An edge into a node that isn't on this canvas is a grey dashed stub labelled with the target.

- [S-3] **Compatibility.** The catalogue (`Targets` of each output) is the single source; the table below is a summary. Decision 3 is implemented by migration `00041` (inbound routes accept `ring_group`, `voicemail`, `announcement` and `paging` besides the older kinds), on top of the destination kinds the IVR, time condition and queue migrations added.

  | Output \ input                                                                                | Ext `ring` | Ext `voicemail` | RG `ring`                                                     | IVR                                       | Queue | Ann | Paging | Conf | TC  | External | SIP URI | DID-as-ext |
  | --------------------------------------------------------------------------------------------- | ---------- | --------------- | ------------------------------------------------------------- | ----------------------------------------- | ----- | --- | ------ | ---- | --- | -------- | ------- | ---------- |
  | Inbound `destination`                                                                         | ✓          | ✓               | ✓ (`ring_group`; `extension` also takes a numeric group name) | ✓                                         | ✓     | ✓   | ✓      | ✓    | ✓   | ✓        | ✓       | ✓          |
  | Extension forwards                                                                            | ✓          | –               | ✓ when numeric                                                | –                                         | –     | –   | –      | –    | –   | ✓        | –       | –          |
  | Ring group `noAnswer`                                                                         | ✓          | ✓               | ✓                                                             | ✓                                         | ✓     | ✓   | –      | –    | ✓   | ✓        | –       | –          |
  | IVR keys / `timeout` / `invalid`, queue `timeout`/`full`/`noAgents`, TC `open/closed/holiday` | ✓          | ✓               | ✓                                                             | ✓ (a menu's own key may return to itself) | ✓     | ✓   | ✓      | –    | ✓   | ✓        | –       | –          |
  | Feature code `target`                                                                         | –          | –               | –                                                             | ✓                                         | –     | ✓   | –      | –    | ✓   | –        | –       | –          |

  **Deviation from decision 3:** a call that has already started cannot be handed to a paging group or a conference room (the runtime pages and joins only from the INVITE), so ring group fallbacks and every exit of an IVR, queue or time condition do not offer those two; and extension forwards stay what the stored number can express (a forward to an IVR menu, queue or time condition is not stored; hello-sip resolves a forward as ring group, then extension, then outbound route). Park is not a destination at all: a call is parked by a feature code or a transfer to a slot. These are runtime limits, not catalogue choices; the catalogue lists exactly what is stored.

  **Ring group fallback to an extension or another group** (failure kinds `extension` and `ring_group`, migration `00043`) is offered. The ring state machine opens a new ring stage for it (`groupFailover` in `internal/sip/groups.go`): the first stage's legs are abandoned and cancelled, the call gets a fresh stage number and setup channel, and the target is rung. An extension target keeps its own features (do not disturb and no registered device go to its voicemail box, a ring that is not answered follows its no-answer forward or voicemail, the box being the target's); a group target runs with its own strategy (ring-all or hunt) and failure destination. Loops are cut with 408 "forward loop": a group or extension already rung in the chain, a group naming itself, or a chain longer than `groupFailMaxDepth` (the queue failover bound, 8). The API refuses a group naming itself and a target that does not exist, and the store refuses to delete, rename or renumber what a group falls back to. An IVR menu that rings a group applies the same fallbacks to the menu's caller.

  Resource ports: `audio` → IVR `greeting/invalidPrompt/timeoutPrompt`, queue prompts, MOH `tracks`, conference announcements; `member` → RG `members`, Paging `members`/`callers`, queue `members`, pickup group `members`; Trunk/Any-trunk `inbound` → Inbound `trunk`; Dial plan → Outbound `dial`; Outbound `trunks` → Trunk `carrier`; MOH `use` → `holdMusic`; `schedule` `use` → TC `schedule`; calendar `use` → schedule `calendars`; Paging `multicast` → multicast channel `page`; Device → Extension `devices`; Phone → Device. Anything else is refused, with a tooltip saying why ("Ring group names must be numeric to be dialled from a forward").

- [S-4] **Edge ownership.** Every edge is stored in exactly one field of one object, its _owner_. The graph lists `owner: {node, field}` on every edge, and connecting or disconnecting becomes one update of the owner. Owners: inbound route (`trunkId`, `destinationKind`+`destination`), outbound route (`trunks[]`), extension (`forward*`), ring group (`members[]`, `failureKind`+`failureTarget`), paging group (`members[]`, `callers[]`), IVR (`options[]`, `timeout`, `invalid`, `*PromptId`, `greetingId`), feature codes (`argument`; the PUT replaces the whole table), device (`extensionId`), phone (`deviceId`), time condition (`open`/`closed`/`holiday`, `scheduleId`), schedule (`calendarIds`), queue (`members[]`, `timeout`/`full`/`noAgents`, `*PromptId`, `breakoutVoicemail`, `mohClassId`), MOH class (`announcementIds`) and the objects that use hold music (`mohClassId` on extension, ring group, inbound route and queue), pickup group (`members[]`), conference (`entryAnnouncement`, `exitAnnouncement`), paging group (`multicastChannelId`).
- [S-5] **Name resolution.** Target strings resolve with the same rules hello-sip uses: ring group name first, then extension number, then IVR `number`. Kinds that name things (`ring_group`, `ivr`, `announcement`, `paging`) resolve directly. Anything else becomes `external`/`sipUri`. A target that resolves to nothing becomes a `missing` node with error `dangling_reference`.

### Source of truth and layout

- [S-6] **The canvas is a view.** `GET /api/v1/flow/graph` builds the graph from the live tables in one `REPEATABLE READ` read transaction and returns `configRevision`, the nodes (id `<kind>:<id>`, or `<kind>:<key>` for codes and terminals; label; summary fields; `version` = sha256 of the object's GET representation, first 16 hex chars) and the edges (id, source/target node+port, owner, edge data such as position/weight/order). No configuration is stored twice. Query `scope=canvas:<id>` returns the nodes on that canvas plus one hop of stubs; `scope=closure:<nodeId>` returns everything reachable from a node; `scope=all` returns everything except devices and phones unless `include=devices`.
- [S-7] **Canvases and positions** are the only new state (migration `000NN_flow_canvas.sql`, numbered after the branches merge): named canvases shared by all users, each holding node positions. Writing layout is not a configuration change: no revision bump, no hello-sip notify, an audit row `flow_canvas` `layout`. Layout for an object that no longer exists is ignored on read and pruned whenever a batch applies or the layout is saved.
- [S-8] **Objects not on the canvas** stay fully configured and work. The left sidebar lists them by type under "Not on this canvas" (searchable), and they can be dragged in. An edge to an off-canvas object shows as a stub that adds it on click. "Import flow" takes a trunk, DID or feature code, places its whole reachable graph with dagre (left to right; ranks: entry → routes → branches → targets → terminals), and only places nodes not already on the canvas.
- [S-9] **Concurrent edits.** The canvas polls `GET /api/v1/flow/revision` every 10 s (`usePolling`). With no draft it reloads silently. With a draft it shows "Configuration changed (revision N → M)", reloads, and rebases the draft: each staged op whose target `version` still matches is kept, and the others are marked as conflicts on their nodes (keep mine / take theirs). The server makes the final decision at apply time (S-13). Form pages, MCP clients and AI proposals all write through the same `configChange`, so every one of them advances the same revision.

### Editing

- [S-10] **Draft.** All edits go into a client-side draft: an ordered list of ops `{ref?, operationId, pathParams, body, expect?}`, the same shape as proposal actions. Edits made in the UI:
  - dragging a node type from the palette opens the panel in create mode, and its op uses a temporary `ref`;
  - drawing an edge stages an update of its owner;
  - deleting an edge stages an update that clears the field (the kind goes back to `none`/`hangup` where the model has one, otherwise the edge can't be deleted and the tooltip says to rewire it);
  - deleting a node stages the delete op, and the confirm lists everything that references it (from the graph's incoming edges) along with the updates that will clear them;
  - renaming a referenced object stages updates to every referrer (S-5 rename cascade).
  - Draft nodes are drawn with a dashed outline. The draft survives a reload: it is kept in `localStorage` per canvas and user, together with its base revision.
- [S-11] **Undo/redo.** Before apply, Ctrl/Cmd+Z and Shift+Ctrl/Cmd+Z step through the draft history (a pure reducer, so it can be unit tested). Layout moves also go into the history. After apply, "Revert" on a batch (S-14) previews and applies the inverse batch.
- [S-12] **Validation on the canvas.** A debounced dry-run (S-13, `dryRun: true`, at most one in flight, 800 ms) runs while a draft exists. Errors come back as `{opIndex, node, path, message}`, so they appear as a red badge on the node and the field in the panel, mapped with `forms.mapFieldErrors`. Warnings come back as `{node, code, message}`:
  - `unreachable`: no entry path;
  - `dangling_reference`;
  - `loop`: an IVR or time-condition cycle with no exit, or a forward chain back to itself;
  - `empty_group`: no members;
  - `shadowed_route`: an inbound route whose DID an earlier route always matches, reusing the AI detector's logic;
  - `no_greeting`;
  - `voicemail_disabled_target`.
    Warnings never block Apply; errors do.
- [S-13] **Apply.** Apply first shows the diff preview: a per-op field-level before → after list, with nodes highlighted on the canvas (green created, amber changed, red deleted). Confirming sends `POST /api/v1/config/batch` with `baseRevision` and the canvas layout of new nodes. All of it applies or none of it does. `409 config_stale` marks the conflicting nodes, and the draft is kept.
- [S-14] **Batch history.** Each applied batch is a `config_batches` row (ops, per-op before/after, actor, via, revisions). `GET /api/v1/config/batches` lists them, and `POST /api/v1/config/batches/{id}/revert` with `dryRun` builds the inverse: updates restore `before`, creates are deleted, deletes are re-created from `before` with a new id and their layout moved over. The inverse is checked for staleness the same way.

### Right-hand panel

- [S-15] **The existing forms, extracted.** Each page's private drawer (`RingGroupDrawer`, `TrunkDrawer`, `OutboundDrawer`/`InboundDrawer`, `ExtensionDrawer`, `PagingDrawer`, the IVR editor, announcements) becomes an exported `XxxForm` in `web/src/forms/` with props `{value, mode: "live" | "draft", onSubmit(body), serverErrors, onCancel}`. Its validation helpers (`validateGroup`, `groupBody`, `groupKeys`, …) move with it. In live mode the page passes the API call it makes today. On the canvas, draft mode stages an op ("Stage change"). The canvas puts the form in the design-system `Drawer` with `inline`, 440 px wide, sliding in from the right, closed with Esc. The page tests keep passing unchanged, which proves the extraction kept the behaviour.
- [S-16] **Panel parts specific to the canvas.**
  - A selected edge shows its owner field, with an editor for edge data: member position, weight and delay; trunk order.
  - The DestinationPicker (from `ivr`) stays in the forms, so connecting with the keyboard is a field edit. This is the accessible alternative to dragging.
  - Show-once secrets (trunk password, device secret) are edited only on their pages; the panel links there.

### AI agent

- [S-17] **Read tools.** `getFlowGraph`, `getFlowCatalogue`, `listFlowCanvases` and `traceFlow` are `GET` operations with scope `read`, role `viewer`, MCP-exposed (automatic from OpenAPI) and added to `assistantTools`. `getFlowGraph` results stay under the 16 KiB tool limit through `scope=closure:<nodeId>` and `fields=summary`.
- [S-18] **Trace.** `GET /api/v1/flow/trace?from=trunk:<id>|<extension>&number=<did>&callerId=&at=<RFC3339>&digits=<key sequence>` runs `routing.Decide` (as `routingTest` does), then walks the graph:
  - extension: forwards and DND, ring, then the no-answer forward or voicemail;
  - ring group: members, then the `noAnswer` branch;
  - IVR: the key given in `digits`; with no digits, every configured key is listed as a branch, to depth 6;
  - time condition: evaluated in-process with the same function behind `time-conditions`' evaluate endpoint at `at`, including the override (the `timecond` package's `Resolve`).
    Returns ordered steps `{node, port, text, at?, branches?}`. The canvas trace mode highlights the path and gives a time picker and a keypad. "What happens when X calls DID Y at 20:00 Sunday" is a single call.
- [S-19] **Proposals on the canvas.** `applyConfigBatch` joins `proposal.Allowlist` as one action whose body is a batch. Validation recurses: every inner op must be on the inner allowlist, which is the existing allowlist plus IVR, time-condition and MOH writes and, if decision 2 allows, `createExtension`/`createIvrMenu`/`createTimeCondition`. The credential-property rule applies. There are at most 50 inner ops. The batch dry-run replaces the routing-only dry-run. Opening a proposal that carries a batch shows it on its canvas (`/flow/:canvasId?proposal=<id>`) as a ghost overlay, using the S-13 diff colours. Apply and dismiss are the existing `applyAIProposal` and `dismissAIProposal`, so the replay uses the applier's credentials and the stale and audit rules are unchanged. The canvas side panel can open the assistant with the canvas and the selected nodes as context.
- [S-20] **External agents.** `applyConfigBatch` (scope `write`) and `putFlowLayout` are MCP tools for write-scoped clients. A new skill, `skills/hello-flow` ("dry-run, read the diff, apply, then traceFlow to verify"), is checked by `TestSkills`. The in-product assistant gets no write tool (`ai-agent` S-11 is unchanged).
- [S-21] **Permissions.** Viewers: view, trace, read the catalogue. Operators: draft, layout, apply. Every inner op is replayed through `api.Handler`, so each one needs its own route's scope and role: a batch with `createPagingGroup` needs `admin`, and if any op fails that check the whole batch is rejected with nothing applied. The batch route itself is `write`/`operator`. Ops whose response carries `x-hello-secret`, multipart ops and `admin`/`secrets`-scoped credential ops are refused inside a batch with `400 op_not_batchable`.

## Decisions

Answered by the user on 2026-10-25 (also in `.procoder/ask/decisions.md`). They override any earlier wording in this spec.

1. **Canvases:** many named, shared canvases (for example "Main office", "After hours"). An object may appear on several. A computed "everything" view is never stored.
2. **AI has full access.** The in-product assistant and write-scoped MCP clients may build, change and apply canvas changes directly, for any object type including devices, phones and trunks. Safety comes from the following rules:
   - every AI change goes through the same transactional batch endpoint (`POST /api/v1/config/batch`);
   - it is audited with actor `ai` and shown on the canvas;
   - it is revertible: the applied batch can be undone with `POST /api/v1/config/batches/{id}/revert`;
   - secrets (SIP passwords, trunk credentials) are never returned to the model. The AI may set or generate them; reads return redacted values.
3. **Destinations are unified.** Every "call goes to..." output accepts every call target kind: extension, ring group, queue, IVR, voicemail, announcement, paging, time condition, external number, park and so on. A migration extends inbound routes and ring-group fallback accordingly.
4. **Renames cascade.** The batch rewrites every object that references a renamed one.
5. **Saving is always draft plus Apply** with a diff, undo/redo, live validation, one transaction and later revert.

Consequences for the sections above, where they differ:

- S-19, S-20 and the "in-product assistant gets no write tool" line of S-20 are replaced by decision 2: the assistant gets `applyConfigBatch` as a write tool, in addition to proposals. Its batches use the same dry-run, staleness and audit rules and carry `via` `ai:<session>`. The inner allowlist covers every object type, and the credential-property rule changes to: secret fields may be written but are redacted in every result, diff, audit row and tool output the model sees.
- S-21: `op_not_batchable` still refuses multipart ops and ops with external effects. Credential ops (`rotateDeviceSecret` and similar) become batchable for the AI path only when their response secret is stripped before it leaves the server.
- S-3: the **Q3** marks are now allowed; the unification migration (`00041`) is done, with the deviation noted under S-3; the ring group fallbacks to an extension or another group followed in `00043`.
- S-1: queues, park lots, pickup groups, conferences and multicast channels are catalogue node types (all merged and wired).

## Migrations

The in-flight branches this section once listed (`00017` to `00023`) merged under other numbers (`00029` to `00040`). Numbers are taken when a migration merges: highest on main plus one.

- `00041_flow_destinations.sql` (P1, decision 3): widens `inbound_routes.destination_kind` (`ring_group`, `voicemail`, `announcement`, `paging`) through `pg_temp.hello_check_values`, reading the live constraint so other migrations' values are kept. No row changes, so it is safe on a live database. Down: `ring_group` inbound routes become `extension` routes (the same call, as a group name is looked up before an extension number); `voicemail`, `announcement` and `paging` routes are removed, as the earlier destination migrations do.
- `00043_group_failover.sql`: widens `ring_groups.failure_kind` with `extension` and `ring_group` through `pg_temp.hello_check_values`. No row changes. Down: groups with those fallbacks go back to `none`.
- The canvas, layout and batch tables (`flow_canvases`, `flow_canvas_nodes`, `config_batches`) take the next free numbers when P2 and P3 merge.

## Out of scope

- Devices and phones on the canvas by default (shown behind a toggle in a later phase); creating devices, phones or trunks with credentials from the canvas.
- Live call animation on the canvas (a later idea; the live-state data exists).
- Moving name-based references to ID foreign keys (a later spec).
- Real-time co-editing (cursors, CRDT): the revision with rebase is enough for a PBX's few editors.
- Uploading announcement audio from the canvas: the panel links to Announcements; TTS creation can come once `tts-prompts` lands.
- Replacing the form pages: they stay and share the extracted forms.

## Constraints

- **Dependencies (web):** `@xyflow/react` `^12.12.0` (MIT; npm latest 2026-09-24; peers `react >=17`, brings `zustand ^4`, `classcat`, `@xyflow/system`), `@dagrejs/dagre` `^3.1.3` (MIT, 2026-10-24). `elkjs` 0.12.0 is rejected because its licence (EPL-2.0 or GPL-3.0-or-later) is copyleft and Hello is Apache-2.0; `reaflow` (Apache-2.0), `rete` 2 and `@antv/x6` (MIT) were considered and rejected for smaller React-19 communities and heavier APIs. No Pro-only xyflow features. The canvas route is lazy-loaded so the main bundle doesn't grow.
- **Dependencies (Go):** none new.
- **One source of truth:** the catalogue in Go; the UI never hard-codes compatibility.
- **No hot-path impact:** graph, trace and dry-run read the store; hello-sip only sees the single revision bump per batch.
- **Performance:** graph build < 200 ms with 10 000 extensions and 1 000 routes (benchmark); the canvas stays above 50 fps at 500 nodes with `onlyRenderVisibleElements`; a dry-run < 300 ms for 50 ops.
- **Accessibility:** every connection can also be made in the panel's DestinationPicker; nodes are focusable, the arrow keys move between them, Enter opens the panel; port types carry shape as well as colour.
- **Gate per task:** as `ai-agent` (gofmt, vet, golangci-lint, `go test -race`, `procoder check/test/lint`), plus mutation checks on the compatibility check, staleness and batch atomicity.

## Interfaces

- **HTTP (all in `openapi.json`, each with `x-hello-scope`/`x-hello-role`/`x-hello-mcp`):**
  - `GET /api/v1/flow/catalogue` (`getFlowCatalogue`, read/viewer)
  - `GET /api/v1/flow/graph?scope=&include=&fields=` (`getFlowGraph`, read/viewer)
  - `GET /api/v1/flow/revision` (`getFlowRevision`, read/viewer): `{configRevision}`
  - `GET /api/v1/flow/trace?from=&number=&callerId=&at=&digits=` (`traceFlow`, read/viewer)
  - `GET|POST /api/v1/flow/canvases`, `GET|PATCH|DELETE /api/v1/flow/canvases/{id}` (read/viewer; write/operator)
  - `PUT /api/v1/flow/canvases/{id}/layout` (`putFlowLayout`, write/operator): `{layoutVersion, nodes:[{node, x, y, collapsed}], remove:[node]}`; `409 layout_stale` when `layoutVersion` has moved, and the client merges per node
  - `POST /api/v1/config/batch` (`applyConfigBatch`, write/operator)
    - Request: `{baseRevision, dryRun, canvasId?, ops:[{ref?, operationId, pathParams, body, expect?}], layout?:[{node, x, y}]}`. `"${ref.id}"`/`"${ref.name}"` placeholders in later ops are filled in from earlier results.
    - Response `200`: `{applied, batchId?, revision, results:[{ref?, operationId, status, id?, before, after, diff:[{path, before, after}]}], warnings:[…]}`.
    - Errors: `422 validation_failed` `{errors:[{opIndex, node, path, message}]}`, `409 config_stale` `{conflicts:[{opIndex, node}]}`, `403` naming the failing op, `400 op_not_batchable`, `413` over 200 ops.
  - `GET /api/v1/config/batches`, `GET /api/v1/config/batches/{id}`, `POST /api/v1/config/batches/{id}/revert` (`{dryRun}`)
- **Go:**
  - `internal/flow`: catalogue, graph builder, resolver, validators/warnings, trace.
  - `internal/api/flow_handlers.go`, `config_batch.go`.
  - `store.WithBatch(ctx, tx)`: `configChangeID` joins an open batch transaction found in the context. It skips its own lock, the per-op `Check` and the revision bump; it writes its audit row with `via` `batch:<uuid>`. The batch runs the baseline-relative `Check` once at the end, bumps the revision and notifies once.
  - The batch replays each op through `api.Handler` with the caller's `Cookie`/`Authorization` (the `internal/replay` path). A dry-run rolls the transaction back.
- **UI routes:** `/flow` (canvas list), `/flow/:canvasId`; a nav item "Flow designer" in the call-flow section. Code in `web/src/pages/flow/` (`FlowCanvas.tsx`, `nodes/*.tsx`, `draft.ts` reducer, `catalogue.ts`, `layout.ts` dagre) and `web/src/forms/*Form.tsx`.
- **Metrics:**
  - `hello_flow_graph_build_seconds`
  - `hello_config_batches_total{result="applied|dry_run|stale|invalid|forbidden"}`
  - `hello_config_batch_ops` (histogram)
  - `hello_flow_trace_total`

## Data

- **PostgreSQL**, migration `000NN_flow_canvas.sql` (with down):
  - `flow_canvases`: `id bigserial`, `name text unique` (1–64), `description text`, `viewport jsonb`, `layout_version bigint default 0`, `created_by text`, `created_at`, `updated_at`.
  - `flow_canvas_nodes`: `canvas_id` references `flow_canvases` on delete cascade, `node_id text` (`<kind>:<id|key>`, CHECK on the kind list), `x real`, `y real`, `collapsed bool`; primary key `(canvas_id, node_id)`.
  - `config_batches`: `id uuid`, `actor`, `via`, `base_revision`, `revision`, `ops jsonb`, `results jsonb` (before/after per op), `reverts uuid null`, `created_at`; index on `created_at desc`; pruned after 90 days.
  - `audit_events.via` carries `batch:<uuid>`. The outer origin (UI, an MCP client id, `ai-proposal:<id>`) is stored on the `config_batches` row.
- **Browser:** the draft in `localStorage` key `hello.flow.draft.<canvasId>.<user>`: `{baseRevision, ops, history}`.

## Edge cases

- **Rename of a referenced ring group, IVR or announcement:** the canvas stages the rename plus an update of every referrer, all in one batch. A rename through a form page or the API leaves dangling references, which the canvas shows as `missing` nodes with a "relink" action.
- **A ring group with a non-numeric name connected from an inbound route or forward:** refused, with the reason; the panel offers to rename it. This goes away under decision 3.
- **Two inbound routes match the same DID:** both are drawn; the shadowed one gets the `shadowed_route` warning; the order badge shows its position, and reordering is a `PUT /routes/inbound/order` op.
- **An IVR option pointing at its own menu:** refused by the catalogue (the `ivr` branch already filters `selfName`); a longer cycle is a `loop` warning, not an error, because callers can still leave through `timeout`/`invalid`.
- **Deleting an extension that is a ring group member** (FK without cascade): the delete confirm lists the groups and stages their member updates first; without them the batch gets the store's error mapped to the node.
- **Feature codes** are one `PUT` of the whole table: two staged feature-code edits merge into one op.
- **A batch mixing a create with a later op referencing it:** placeholders resolve in order; an unresolved placeholder is `422` before anything runs.
- **Op 7 of 10 fails:** the transaction rolls back; revision unchanged; no audit rows; the response names op 7.
- **The revision moves between dry-run and apply** with no overlapping target: applied, as for proposals; overlap: `409 config_stale`.
- **Layout for a node deleted by someone else:** ignored on read; the stub disappears; pruned on the next layout write.
- **A viewer opening a proposal overlay:** sees it, gets no apply button.
- **An AI batch with 60 ops:** rejected by the proposal validator (cap 50) and returned to the model as a validator error.
- **Time-condition override active:** the trace and the node badge show "forced closed until …".

## Failure modes

- **PostgreSQL unavailable:** graph, trace and batch return `503`; the canvas keeps the draft locally and shows the error.
- **The graph is too large for `scope=all`:** server cap of 5 000 nodes, then `413 graph_too_large` suggesting `scope=closure`.
- **dagre layout of a large import is slow:** runs in a Web Worker; a progress note is shown; cancellable.
- **A handler with side effects outside the database inside a batch** (MinIO, push): prevented by `op_not_batchable` (multipart ops and ops with external effects are listed explicitly; `TestBatchableOps` fails if a batchable op's handler touches MinIO).
- **hello-sip misses the notify:** unchanged from today; its periodic snapshot check picks up the single revision.

## Phasing

- **P0 — graph model (read-only), no migration.** Delivered in PR flow-canvas-p0: `internal/flow` (node and port catalogue, graph builder with owners, name resolution and warnings, closure scope), `getFlowCatalogue`, `getFlowGraph`, `getFlowRevision` in `openapi.json`, unit tests.
- **P1 — destination unification and graph hardening. Delivered (PR flow-canvas-p1).** Migration `00041` (decision 3) with the API validation, the routing table (`routing.Config.Named`, `Decision.DestKind/DestTarget`), the `routingTest` result and hello-sip dispatch for the new kinds (inbound routes use the destination dispatcher time conditions use); golden fixtures (`internal/flow/testdata`); `TestCatalogueDrift`; `TestGraphBench`; the warnings `unreachable`, `loop`, `shadowed_route` and `no_greeting`; and the guards listed in `internal/flow/README.md` that fail CI when a feature merges without canvas support. The Routes form page offers the new inbound kinds since the follow-up that fixed it.
- **P2 — read-only canvas.** Add `@xyflow/react` and `@dagrejs/dagre`; canvases and layout migration and API; canvas list, the canvas, the "not on this canvas" sidebar, stubs, import with auto-layout, warnings, panel in read-only view mode. Useful on its own as a map of the PBX.
- **P3 — batch backend. Delivered (PR flow-canvas-p3).** `store.RunBatch` and the batch-aware store (`internal/store/batch.go`: every write made under the batch's context joins its transaction, the baseline-relative `Check` runs once at the end, one revision bump and one notify), the batch handler with placeholders, dry-run, `expect` staleness, layout and pruning, `config_batches` (migration `00049`), revert with its staleness check (`internal/api/config_batch.go`, `internal/configbatch`), the four OpenAPI operations (MCP tools `applyConfigBatch`, `listConfigBatches`, `getConfigBatch`, `revertConfigBatch`), `docs/config-batch.md`, and the guards `TestBatchableOps`, `TestBatchCoverage`, `TestBatchableHandlersNoExternalEffects`. Choices the spec left open are in `.procoder/ask/decisions.md`.
- **P4 — editing UI.** Draft reducer, palette create, connect and disconnect from the catalogue, delete with reference listing, rename cascade, panel in draft mode, undo/redo, debounced dry-run validation, diff preview, apply, rebase on revision change.
- **P5 — trace.** `traceFlow` with routing, extension, ring group, IVR and (the time condition node having merged) time conditions; canvas trace mode.
- **P6 — AI.** `assistantTools` plus graph and trace; `applyConfigBatch` in the proposal allowlist with recursive validation; the proposal overlay on the canvas; assistant context from the canvas; skill `hello-flow`; `docs/flow-canvas.md`.
- **P7 — new node types, per branch as it merges.** The graph side is delivered with P1 for every merged feature: IVR menus and options, time conditions, schedules and holiday calendars, MOH classes and `holdMusic` inputs, queues and queue agents, conferences, park lots, pickup groups, multicast channels, the TTS-generated announcement badge, and the feature-code actions `ivr` and `time_condition` (the others are classified in `FeatureCodeNoTarget`). Still to do in the later phases: batchable ops for the new objects (P3/P6), time conditions in the trace (P5, "evaluate" at `at`, including the override), and "generate prompt" in the panel (P4). A checklist in `internal/flow/README.md` makes each new node mechanical.
- **Dependencies:** P1 and the graph side of P7 are done. The guards (`TestCatalogueDrift`, `TestResourceCoverage`, `TestNoStaleRequires`) make a future feature fail CI until it is on the canvas or on a reasoned list.

## Acceptance criteria

- [x] [S-1] [S-3] `TestCatalogueDrift` in `internal/flow` passes (database part in the `integration` job). It fails if any value of a destination-kind CHECK constraint (read with `pg_get_constraintdef`) or OpenAPI enum has no catalogue port, or a catalogue connection maps to a kind the database refuses.
- [x] [S-4] [S-5] [S-6] `TestGraphGolden` passes. Fixture configurations (simple office, multi-level IVR, dangling reference, numeric and non-numeric ring group, any-trunk route) produce the stored graph JSON. It fails if an edge lacks its owner or a target resolves differently from `snapshot` lookup order.
- [x] [S-4] [S-13] `TestEdgeRoundTrip` passes. For every connection in the catalogue, applying the owner update via batch then reading the graph shows the edge, and clearing it removes it.
- [x] [S-13] `TestBatchAtomic` and `TestBatchStale` in `internal/api` pass. They fail if a failing op leaves any change, revision bump or audit row, a dry-run commits, an unrelated revision change rejects, or an overlapping one applies.
- [x] [S-21] `TestBatchRoles` passes. It fails if an operator's batch containing an admin-only op applies anything, or a batch carries a secret-returning, multipart or credential op.
- [ ] [S-7] `TestLayout` passes. It fails if a layout write bumps the config revision, a stale `layoutVersion` is accepted, or layout of a deleted object is returned.
- [x] [S-14] `TestBatchRevert` passes. It fails if a revert's dry-run commits, a stale revert applies, or a re-created object loses its layout.
- [x] [S-18] `TestTrace` passes. It covers a DID at a time outside an inbound schedule, an IVR with and without `digits`, ring group failover to voicemail and, after P7, a time condition at 20:00 Sunday with and without override. It fails if a step names the wrong node or port.
- [ ] [S-17] [S-19] [S-20] `TestAssistantTools`, `TestProposalValidation` and `TestSkills` pass with the new tools. They fail if an inner op outside the inner allowlist, a credential property, or more than 50 inner ops is stored.
- [ ] [S-10] [S-11] [S-12] [S-15] [S-16] `procoder test` passes over `web/`. `draft.test.ts` fails if undo/redo doesn't restore the exact op list, the rebase keeps a conflicting op, or edits to feature codes don't merge into one op. `FlowCanvas.test.tsx` fails if an invalid connection is accepted, a viewer sees edit controls, apply is sent without a diff confirmation, or an error is not shown on its node. The existing page tests pass unchanged after the form extraction.
- [ ] `TestFlowEndToEnd` in `test/integration` passes. It builds trunk → DID → IVR → ring group → voicemail with one batch, places a call through the trunk fixture pressing `1`, and fails if the call does not reach the group.
- [x] `TestGraphBench` (`HELLO_BENCH=1`) passes (about 50 ms at 10 000 extensions). It fails if the graph build takes 200 ms or more at 10 000 extensions.
