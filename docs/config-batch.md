# Configuration batches

A batch applies many configuration changes in one database transaction. It is
the backend of the call-flow canvas (spec `.procoder/specs/flow-canvas.md`,
S-13 and S-14) and is open to any client with the `write` scope and the
operator role, including MCP clients (`applyConfigBatch`).

| Operation           | Route                                     | What it does                              |
| ------------------- | ----------------------------------------- | ----------------------------------------- |
| `applyConfigBatch`  | `POST /api/v1/config/batch`               | Apply (or dry-run) a list of operations   |
| `listConfigBatches` | `GET /api/v1/config/batches`              | Applied batches, newest first             |
| `getConfigBatch`    | `GET /api/v1/config/batches/{id}`         | One batch with its operations and results |
| `revertConfigBatch` | `POST /api/v1/config/batches/{id}/revert` | Undo a batch (with `dryRun` to preview)   |

## Request

```json
{
  "baseRevision": 41,
  "dryRun": false,
  "canvasId": 3,
  "ops": [
    {
      "ref": "rg",
      "operationId": "createRingGroup",
      "body": {
        "name": "sales",
        "members": [{ "extensionId": 7, "position": 1 }]
      }
    },
    {
      "operationId": "updateInboundRoute",
      "pathParams": { "id": "12" },
      "expect": "3f2a9c1b7d4e8a60",
      "body": { "destinationKind": "ring_group", "destination": "${rg.name}" }
    }
  ],
  "layout": [{ "node": "ringGroup:${rg.id}", "x": 320, "y": 140 }]
}
```

- `baseRevision` is the `configRevision` the batch was prepared against
  (`GET /api/v1/flow/revision` or `getFlowGraph`).
- Each operation is an `operationId` with its `pathParams` and `body`, exactly
  as that operation documents them. The object `id` of an update or delete is
  the path parameter `id`.
- `ref` names an operation's result. Later operations, and `layout` nodes, use
  `${ref.field}` in path parameters and body strings. A string that is exactly
  one placeholder keeps the value's type (`"${rg.id}"` becomes the number 12);
  inside longer text it is replaced as text. A placeholder that names no
  earlier result, or a field the response does not have, is `422` before
  anything runs.
- `expect` is the node `version` from `getFlowGraph`. It matters only when the
  configuration moved since `baseRevision`: an update or delete whose target's
  version differs is a conflict. Operations without `expect`, and operations on
  an object the same batch created, are never conflicts.
- `layout` places new nodes on `canvasId`. Saving layout is not a
  configuration change; positions of nodes that no longer exist are pruned from
  every canvas whenever a batch applies.
- At most 200 operations (`413` beyond that).

## What may be in a batch

The object writes of the canvas: extensions, devices, phones, trunks, inbound
and outbound routes (and their order), ring groups, IVR menus, time
conditions, schedules, holiday calendars, music on hold classes, queues, park
lots, pickup groups, conferences, multicast channels, paging groups and the
feature codes (`putFeatureCodes`). The list is `Ops` in
`internal/configbatch/ops.go`; anything else is `400 op_not_batchable`:

- operations whose response carries a secret (`createDevice`, `createPhone`),
  credential operations (rotate and reveal, tokens, users, service accounts,
  settings) and every operation that needs the `secrets` or `session` scope;
- multipart operations and operations that touch object storage, a speech
  provider, a phone or live state in Valkey (announcements, voicemail
  greetings, firmware, queue agent login, conference control, time condition
  override, calendar import): their effects would not roll back;
- operations on objects the canvas does not draw (phonebooks, users, messages).

Every operation is replayed through the API with your own credentials, so it
needs its own route's scope and role. A batch with `createPagingGroup` needs
the admin role; if any operation is refused the whole batch is rejected with
`403`, naming the operation, and nothing is applied.

## What happens

1. One transaction takes the configuration lock. If the revision is not
   `baseRevision`, every operation with `expect` is checked against its
   target's current version; any difference is `409 config_stale` with
   `conflicts: [{opIndex, node}]`.
2. The operations run in order. The first failure rolls everything back and is
   reported with its index: `422 validation_failed` with
   `errors: [{opIndex, node, path, message}]` for a rejected value, or the
   operation's own `403`.
3. The whole-configuration routing check runs once, against the configuration
   before the batch: only errors the batch introduces count, and each names the
   operation that made it.
4. With `dryRun` the transaction rolls back and the answer is exactly what an
   apply would return, with `applied: false`.
5. Otherwise the revision moves once, hello-sip is notified once, and the batch
   is stored. Each audit row it wrote has `via` `batch:<id>`; the batch row
   keeps the origin (OAuth client, `ai-proposal:<id>`) and is pruned after 90
   days.

The `200` answer has `applied`, `batchId`, `baseRevision`, `revision`,
`results` (per operation: `status` `created|updated|deleted|unchanged`, `id`,
`node`, `before`, `after` and the field-level `diff`) and `warnings`: the flow
warnings (`unreachable`, `loop`, `dangling_reference`, ...) the batch
introduces. Warnings never block an apply.

Credential values (`password`, `pin`, `secret` and names ending in them) are
withheld from the stored operations.

## Revert

`POST /api/v1/config/batches/{id}/revert` builds the inverse batch and applies
it the same way (it is itself a batch, `reverts` points at the original):

- updates restore the recorded `before` (a PATCH restores only the fields it
  set);
- created objects are deleted;
- deleted objects are made again from their `before`, with a new id; later
  references in the same revert follow the new id, and the node's position on
  every canvas moves to it;
- reorders and the feature-code list are sent again with their `before`.

It is refused with `409 config_stale` (naming the operations) when an object
the batch changed has changed since, compared on the fields its request body
writes. A batch reverts once (`409 conflict` afterwards). A batch that deleted
something a batch cannot make again (a device) is `422`. Credentials a batch
set were never stored, so they are listed in `skipped` and not restored.

## Adding an operation

Add the `operationId` to `Ops` in `internal/configbatch/ops.go` (and to
`Creators` through `crud` when it makes an object). `TestBatchableOps`,
`TestBatchCoverage` (every catalogue node has an operation or a reason in
`NoBatchOps`) and `TestBatchableHandlersNoExternalEffects` (the handler does
not reach object storage, a speech provider, a page test, the queue state or
SMTP) fail CI when something is missing.
