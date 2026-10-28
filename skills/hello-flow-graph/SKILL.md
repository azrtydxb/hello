---
name: hello-flow-graph
description: Read and verify the call flow of a Kuvryn Hello PBX through its MCP server with the flow graph - every trunk, route, ring group, IVR, queue, announcement, paging group and feature code as nodes and edges, plus the shared layout canvases of the Call flows page - to explain where a call ends up, find dangling references and orphaned objects, and prove that a configuration change did what was intended. Use when someone asks "what happens when this number is called", "what uses this object", "is anything broken in my call flow", or after any change to routing, IVR, queues or hours.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello flow graph

The flow graph draws the saved configuration as nodes and edges. It is built
from the live tables, so it always matches what hello-sip will do for the next
call, and it stores nothing. It is the quickest way to see a whole chain at
once; `testRouting` proves one particular call.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Scope `read`, role `viewer` for the graph and the canvases. The changes the
  graph tells you about are made with the tools of the other skills, or all at
  once with a configuration batch (scope `write`, role `operator`, below).

## Tools

| Tool               | Use                                                                                                                                                                                                                                                 |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `getFlowGraph`     | The nodes and edges. `scope` is `all` (default) or `closure:<nodeId>`, everything reachable from one node by following its outputs, for example `closure:inboundRoute:3`. `include=devices` adds devices and phones, which are left out by default. |
| `getFlowCatalogue` | Every node kind with its ports, each port's type and, for every output, the inputs it may connect to. An entry whose `requires` is set cannot be stored until what it names exists.                                                                 |
| `getFlowRevision`  | The configuration revision, which every change advances.                                                                                                                                                                                            |

Every edge names its owner: the one field of one object that stores it. So
"connect A to B" or "disconnect it" is one update of that owner object, with
the update tool of the skill that covers it (`hello-routing` for routes and
ring groups, `hello-ivr-and-hours` for menus and time conditions,
`hello-call-handling` for queues, conferences and paging).

## Canvases: where the nodes sit

A canvas is a named, shared layout for the Call flows page of the console: it
stores only where nodes sit (and the pan and zoom), never the configuration
they draw. Saving a layout is not a configuration change: the revision does
not move and hello-sip is not notified. Use canvases to tidy a picture for
people; never to change a call flow.

- `listFlowCanvases` and `getFlowCanvas` (returns the position of every node
  and a `layoutVersion`).
- `createFlowCanvas` with `{ "name": "Head office", "description": "..." }`,
  `updateFlowCanvas` (rename, description, `viewport` of `x`, `y`, `zoom`) and
  `deleteFlowCanvas` (the configuration it drew is untouched; ask first, other
  users share the canvas).
- `putFlowLayout` sets the position of the listed nodes and drops the listed
  ones: `{ "layoutVersion": 7, "nodes": [ { "node": "inboundRoute:3", "x": 120,
"y": 80 } ], "remove": [] }`. Send the `layoutVersion` you read with the
  canvas; a 409 `layout_stale` means somebody else saved first and nothing was
  stored, so read the canvas again and merge your moves node by node.

## Batches: many changes at once

A configuration batch applies a list of object writes in one transaction: all
of them or none, one revision step, one hello-sip notification. It is what the
canvas editor saves with, and it is the safest way to make a change that spans
several objects (create a ring group and point a route at it).

- `applyConfigBatch` with `baseRevision` (from `getFlowRevision`), `ops` (each
  an `operationId` with its `pathParams` and `body`, exactly as that tool takes
  them; `ref` names a result that later operations use as `${ref.id}` or
  `${ref.name}`; `expect` is the node `version` from `getFlowGraph`), and
  optionally `canvasId` with `layout` to place new nodes. Send `dryRun: true`
  first and show the `results` and `warnings` before applying.
- A `409 config_stale` lists the operations whose target changed since your
  `baseRevision`: read the graph again and rebuild those operations. A `422`
  names the failing `opIndex`; nothing was applied. Operations that carry a
  secret, touch storage or live state, or need the `secrets` scope are
  `400 op_not_batchable`: make those with their own tool.
- `listConfigBatches` (newest first) and `getConfigBatch` (operations,
  results and diffs) show what was applied and by whom.
- `revertConfigBatch` undoes a batch (with `dryRun` to preview). It refuses
  with 409 when an object it changed has changed since; a batch reverts once.
  Ask before reverting: other users may build on the change.

## Workflows

### Explain a number

1. `getFlowGraph` with `scope` `closure:inboundRoute:<id>` (find the route id
   with `listInboundRoutes`) to see where its chain leads: time condition, IVR,
   queue, ring group, extension, voicemail, hangup.
2. Read it back in plain words: "DID 0123 goes to the Main menu during
   office hours, to the Closed announcement otherwise."
3. Prove a real call with `testRouting` (caller `trunk:<id>`, the DID) and,
   for a time condition, `evaluateTimeCondition`.
4. `traceFlow` (`from` an extension or `trunk:<id>`, `number`, optionally `at`
   and the IVR keys in `digits`) walks that call node by node: each step names
   the graph node and the output it leaves through, and a ringing extension
   lists "If busy" and "If not registered" as branches. It places no call.

### What uses this object

Before you delete or rename anything, find its incoming edges in
`getFlowGraph`: their owners are what still names it. The delete tools refuse
with 409 in that case anyway; the graph tells you what to change first.

### Audit

1. `getFlowGraph` (all).
2. Report every `missing` node (a `dangling_reference` warning: a destination
   that names a number, group, queue or announcement that no longer exists).
   Fix each by updating its owner to a valid destination or by recreating the
   missing object.
3. Report objects with no incoming edge that should be reachable: an
   announcement nobody plays, a ring group no route reaches, a queue without
   a route.
4. After the fixes, read the graph again and quote the before and after.

### Check a change

1. Before: note `getFlowRevision` and the closure of the object you will
   change.
2. After: `getFlowRevision` must have moved (otherwise nothing was stored),
   the closure must show the intended edges and no new warnings, and the
   examples you tested with `testRouting` must decide as expected. Poll
   `getFlowRevision` to know whether a graph you read earlier is out of date.

### Before you add a connection

`getFlowCatalogue` says which outputs may connect to which inputs. If the
kind you want is not allowed, the update tool refuses it: choose another
destination kind instead of retrying.

## References

- [Tools](references/tools.md) - the flow tools and the tools they point to.
