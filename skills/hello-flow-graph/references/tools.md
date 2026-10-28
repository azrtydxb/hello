# Tools for the flow graph

| Tool                    | Scope | What it does                                                            |
| ----------------------- | ----- | ----------------------------------------------------------------------- |
| `getFlowGraph`          | read  | The configuration as nodes and edges, whole or the closure of one node. |
| `getFlowCatalogue`      | read  | Node kinds, ports and what may connect to what.                         |
| `getFlowRevision`       | read  | The configuration revision; poll it to see a change land.               |
| `applyConfigBatch`      | write | Apply (or dry-run) many configuration writes in one transaction.        |
| `listConfigBatches`     | read  | Applied configuration batches, newest first.                            |
| `getConfigBatch`        | read  | One batch with its operations, results and diffs.                       |
| `revertConfigBatch`     | write | Undo a batch (with `dryRun` to preview).                                |
| `testRouting`           | read  | Decide one call against the saved configuration with a trace.           |
| `traceFlow`             | read  | The path one call takes through the flow graph, step by step.           |
| `evaluateTimeCondition` | read  | What a time condition does now or at a given time.                      |
| `listInboundRoutes`     | read  | Inbound routes, to find a route id for a closure.                       |
