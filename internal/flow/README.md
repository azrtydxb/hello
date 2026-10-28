# internal/flow

The model behind the flow canvas (spec `.procoder/specs/flow-canvas.md`): a
catalogue of node and port types, and a graph built from the stored
configuration. Nothing here is stored; the canvas is a view.

| File           | What it holds                                                                                                                                                                                  |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `catalogue.go` | The node and port table the UI, the batch validator and the AI read (`GET /api/v1/flow/catalogue`). Compatibility is decided here and nowhere else.                                            |
| `kinds.go`     | The contract with the rest of the code base: stored destination kinds, the columns that hold them, feature-code actions, which configuration objects are nodes and which are deliberately not. |
| `build.go`     | The graph builder: one function per object type, one resolver (`resolve`) for every `(kind, target)` pair.                                                                                     |
| `warnings.go`  | Warnings that need the whole graph (`unreachable`, `loop`, `shadowed_route`).                                                                                                                  |
| `graph.go`     | The graph types, `closure` scope and node versions.                                                                                                                                            |

## Add a node type (checklist)

Do all of it in the same change as the feature; the guards below fail CI when
a step is missing.

1. **Catalogue** (`catalogue.go`): add the `Kind...` constant and a `NodeType`
   with its ports. Inputs say what the object receives (`call`, `members`, a
   prompt, hold music); outputs say what it stores, with `Field` set to the
   owning field (spec S-4) and `Targets` listing every input the stored kind
   may reach. Per-key outputs (an IVR key) set `PerKey`.
2. **Destination kinds**: if the object can be the target of a "call goes
   to..." field, add its stored kind to `StoredKinds` (`kinds.go`) and to the
   `Targets` of every output whose CHECK constraint accepts it. If it has a
   field that stores a destination, add the column to `KindFields` and widen the
   constraint additively in a migration (`pg_temp.hello_check_values`, as
   `00041_flow_destinations.sql`).
3. **Input** (`graph.go`): add the object's list to `Input`, and read it in
   `internal/api/flow_handlers.go` `flowInput` (`TestFlowGraphMergedFeatures`
   fails when a list is left out).
4. **Builder** (`build.go`):
   - register the node and its lookup (by name, number or id) in `addObjects`;
   - draw its edges in an `add...` function, using `b.dest` for a stored
     destination and `b.prompt`, `b.useMOH` or `b.member` for the others. Every
     edge needs an owner: the one field of one object that stores it;
   - add its kind to `resolve` when it is a destination;
   - set `b.entries` when something other than an edge can start a call there
     (a dialled number) and `b.ends` when a call can finish there;
   - put the useful fields in the node `Summary` (never a secret).
5. **Warnings**: dangling references are raised by `resolve`; add the kind to
   the candidates in `unreachable` or `loops` when it can be a dead end.
6. **Resource table** (`kinds.go`): add the OpenAPI collection path to
   `ResourceNodes`. An object that is deliberately not part of a call's path
   goes in `NonFlowResources` with the reason.
7. **Feature codes**: a new action either names a call target
   (`FeatureCodeTargets`, and draw the edge in `addFeatureCodes`) or goes in
   `FeatureCodeNoTarget` with why it has no edge.
8. **Tests**: add the object to a fixture in `golden_test.go`, run
   `go test ./internal/flow -run TestGraphGolden -update`, and read the diff of
   `testdata/*.graph.json`. Add a case to `TestCanConnect` for the new
   connections.
9. **Batch ops** (`internal/configbatch/ops.go`, `docs/config-batch.md`): add
   the object's create, update and delete operations to `Ops` (they must be
   JSON, write- or admin-scoped and return no secret), or list the node kind in
   `NoBatchOps` with the reason. The owner field names the update an edge change
   becomes; `TestEdgeRoundTrip` (in `internal/api`) applies it through a batch.
   `TestBatchCoverage` fails when a node has neither.
10. **Spec**: record the node in `.procoder/specs/flow-canvas.md` (S-1 table).

A feature that lands in a branch before its node does may mark the node or
connection with `Requires` and register the marker in `pendingFeatures`
(`catalogue.go`) with an OpenAPI path that exists once the feature has
shipped.

## Guards

These tests fail CI when a feature is merged without canvas support. They are
in `drift_test.go` and `drift_db_test.go`.

| Test                                                                                                   | Fails when                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestCatalogueDrift/database`                                                                          | A destination kind in a CHECK constraint (read with `pg_get_constraintdef` from a migrated database) has no catalogue node; the catalogue offers a connection the column refuses, or the column stores a kind the catalogue does not offer; a CHECK constraint lists destination kinds and is not in `KindFields`; a feature-code action is unclassified. It needs `HELLO_TEST_DATABASE_URL`, so it runs in the `integration` job and skips on a pull request. |
| `TestCatalogueDrift/openapi`, `/go lists`, `/stored kinds`                                             | The same for every OpenAPI enum of destination kinds, the Go list the IVR API validates against, and the tables in `kinds.go`. They run everywhere. An enum that merely looks like destination kinds is listed in `NotDestinationEnums` with the reason.                                                                                                                                                                                                       |
| `TestResourceCoverage`                                                                                 | An object with CRUD routes in `openapi.json` is neither a node (`ResourceNodes`) nor on the reasoned `NonFlowResources` list, or a catalogue node is drawn from no object.                                                                                                                                                                                                                                                                                     |
| `TestNoStaleRequires`                                                                                  | A node or connection keeps a `requires` marker for a feature whose OpenAPI path already exists.                                                                                                                                                                                                                                                                                                                                                                |
| `TestBatchCoverage` (`internal/configbatch`)                                                           | A catalogue node kind has no update operation in a batch and is not on the reasoned `NoBatchOps` list.                                                                                                                                                                                                                                                                                                                                                         |
| `TestBatchableOps` (`internal/configbatch`), `TestBatchableHandlersNoExternalEffects` (`internal/api`) | A batchable operation is missing from `openapi.json`, returns a secret, is not JSON or write/admin scoped, has no GET to read it, or its handler reaches object storage, a speech provider, a page test, the queue state or SMTP.                                                                                                                                                                                                                              |
| `TestGuardsDetect`                                                                                     | A guard stops detecting the gap it is for (it feeds each one a gap and expects a report).                                                                                                                                                                                                                                                                                                                                                                      |
| `TestGraphGolden`                                                                                      | A fixture configuration no longer produces its stored graph, or an edge lacks its owner.                                                                                                                                                                                                                                                                                                                                                                       |
| `TestGraphBench`                                                                                       | (`HELLO_BENCH=1`, the `bench` job) building the graph takes 200 ms or more at 10 000 extensions and 1 000 routes.                                                                                                                                                                                                                                                                                                                                              |

## Notes

- A port of a per-key output is addressed as `<id>:<key>` (`key:5`); the
  catalogue lists it once as `key` with `perKey: true`.
- Ring group names are looked up before extension numbers, as `sip.ringTarget`
  does. An inbound route of kind `extension` may still name a numeric ring
  group; the newer kind `ring_group` names it directly.
- The catalogue lists exactly what is stored, and the runtime limits what can
  be stored: IVR keys, time condition branches and queue exits cannot go to a
  conference room, a ring group fallback cannot go to a paging group or a
  conference room, because a started call cannot join or be paged into them.
  A ring group fallback can ring an extension or another group: the ring
  state machine opens a new ring stage for it (`groupFailover`). See S-3 of
  the spec.
