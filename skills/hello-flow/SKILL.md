---
name: hello-flow
description: Change a Kuvryn Hello PBX call flow safely through its MCP server - build or rewire trunks, routes, time conditions, IVR menus, ring groups, queues and voicemail as one configuration batch that is dry-run first, read back as a diff and a graph change, applied in one transaction, verified with a traced call and reverted if needed. Use when someone asks to "send this number to a menu after hours", "add a support line", "replace this ring group", "why did this call go there" or to review an AI proposal drawn on the call-flow canvas.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Changing a call flow

One rule: **dry-run, read the diff, apply, then trace to verify.** A call
flow spans several objects (a route, a time condition, a menu, a group), and
a half-made change drops calls. A configuration batch makes all of it or none
of it, and the dry run shows exactly what it will do first.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`).
- Reading the flow and tracing calls: scope `read`, role `viewer`.
- Applying a batch: scope `write`, role `operator`. Every operation inside
  the batch also needs its own role: paging groups, conferences and
  multicast channels need `admin`, and one refused operation refuses the
  whole batch with nothing applied.

## Tools

| Tool                | Use                                                                                                    |
| ------------------- | ------------------------------------------------------------------------------------------------------ |
| `getFlowGraph`      | The flow. `scope=closure:<nodeId>` for one chain, `fields=summary` for a compact read.                 |
| `getFlowCatalogue`  | Which outputs may connect to which inputs; check before you add a connection.                          |
| `traceFlow`         | Walk one call (`from` an extension or `trunk:<id>`, `number`, optional `at`, `callerId`, `digits`).    |
| `applyConfigBatch`  | The change: `baseRevision`, `ops`, `dryRun`. The dry run answers `results`, `warnings` and `flowDiff`. |
| `listConfigBatches` | What was applied, by whom and from where (`via`, such as `ai-proposal:<id>`).                          |
| `revertConfigBatch` | Undo one batch (dry-run it first).                                                                     |
| `getAIProposal`     | A proposal of the in-product assistant, with the batch and the dry run it was checked with.            |
| `listAIProposals`   | Open proposals (`status=open`).                                                                        |

## The workflow

1. **Read.** `getFlowGraph` with `scope=closure:<nodeId>` of where the call
   enters (an inbound route, a trunk) and note `configRevision` and the
   `version` of each node you will change.
2. **Trace before.** `traceFlow` the call the user cares about (for a time
   rule, at a time inside and one outside it; for a menu, with and without
   `digits`). Keep the steps: they are the "before".
3. **Build the batch.** One op per object write, in order; `ref` names a
   created object so later ops use `${ref.id}` or `${ref.name}`. Put the
   node `version` in each op's `expect`. Never put a password, PIN or secret
   in a batch you show to anyone.
4. **Dry-run.** `applyConfigBatch` with `dryRun: true`. Read every result's
   `diff`, every new `warning` (an `unreachable` menu, a `loop`) and the
   `flowDiff` (`added`, `changed`, `removed` nodes and the connections made
   and dropped). Tell the user what will change, in words, before applying.
5. **Apply** the same batch with `dryRun: false` once the user agrees. A
   `409 config_stale` means somebody changed one of your objects: read again
   and rebuild those ops. A `422` names the failing `opIndex`; nothing was
   applied.
6. **Trace after.** `traceFlow` the same calls. The steps must now end where
   the user asked. If they do not, `revertConfigBatch` the batch (dry-run
   first) and explain why.

## AI proposals on the canvas

By default the in-product assistant does not apply a change itself. It
answers a change request with a proposal whose one action is
`applyConfigBatch`; Hello has already dry-run it as the user and drawn it on
the call-flow canvas (`/flow/<canvasId>?proposal=<id>`). To review one:
`getAIProposal` and read the action's `preview` (`results`, `warnings`,
`flowDiff`). Only a person applies or discards it, in the console: applying
and dismissing proposals are not MCP tools (human in the loop). The applied
batch shows in `listConfigBatches` with `via` `ai-proposal:<id>` and reverts
like any other.

When the user turned on **Let AI apply changes** in the canvas, the assistant
applies its batch itself, as that user with their role. Such a proposal is
`applied` with `appliedVia` `ai:<session>` and `batchId`; the batch shows in
`listConfigBatches` with that `via` and the user as `actor`. Revert it like
any other: `revertConfigBatch` with `dryRun` first.

## References

- [Examples](references/examples.md) - complete batches for common changes.
