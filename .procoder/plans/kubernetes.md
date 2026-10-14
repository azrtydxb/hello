# kubernetes — implementation plan

Status: draft
Spec: .procoder/specs/kubernetes.md

## Goal

Hello installs on any Kubernetes cluster from one Helm chart, with documented SIP networking, anti-affinity, highly available PostgreSQL and Valkey guidance, and a rolling upgrade proven to keep an active call.

## Architecture

`deploy/helm/hello` renders the same objects the kw manifests (`deploy/kuvryn-sync/kw/resources.yaml`) declare, parameterised. hello-sip is a StatefulSet over a headless Service: ordered rolling updates replace one node at a time (the ordering the kw preStop stagger enforces by hand), stable pod names give each node its identity and dispatcher name. Kamailio is one Deployment (Recreate) whose dispatcher list is generated from the node count. State services are optional bundled single instances or external references. `test/deploy` renders the chart with `helm template` and asserts invariants; `test/kindupgrade` drives a real upgrade in CI.

## Constraints

- No workloads on the developer machine: only `helm template`, `helm lint` and `kubeconform` run locally; the kind cluster exists only in the CI job.
- kw keeps deploying through Sync; nothing here changes `deploy/kuvryn-sync/`.
- No credentials in values or rendered output: Secrets are referenced, never created.
- Gate per task: `go test ./test/deploy/`, `helm lint --strict`, `procoder check` 0 blocking; REVIEW.md applies.

## Task 1: Chart skeleton and control plane

Files: `deploy/helm/hello/Chart.yaml`, `deploy/helm/hello/values.yaml`, `deploy/helm/hello/templates/_helpers.tpl`, `deploy/helm/hello/templates/control.yaml`, `deploy/helm/hello/templates/ui.yaml`.

- [ ] Chart metadata, values, helpers (names, labels, image refs, shared env), hello-control Deployment with migrate init, hello-ui and the ingresses.

## Task 2: SIP nodes and Kamailio

Files: `deploy/helm/hello/templates/sip.yaml`, `deploy/helm/hello/templates/kamailio.yaml`, `deploy/helm/hello/files/kamailio.cfg`.

- [ ] hello-sip StatefulSet with anti-affinity, PDB, drain-capable termination; Kamailio with wait-dispatcher, checksum annotation, hostNetwork option and generated dispatcher list.

## Task 3: State services and PDBs

Files: `deploy/helm/hello/templates/state.yaml`, `deploy/helm/hello/templates/pdb.yaml`, `deploy/helm/hello/values-ha.yaml`.

- [ ] Bundled PostgreSQL, Valkey, MinIO for evaluation; external references; the HA values file.

## Task 4: Static tests

Files: `test/deploy/helm_test.go`.

- [ ] `helm template` tests for the invariants in the acceptance criteria; helm lint and kubeconform steps in CI.

## Task 5: Guide

Files: `docs/kubernetes.md`, `README.md`.

- [ ] The Kubernetes guide per S-6.

## Task 6: Rolling-upgrade job

Files: `test/kindupgrade/upgrade_test.go`, `.github/workflows/ci.yaml`.

- [ ] kind install, call, `helm upgrade`, assert survival.

## Failure modes

- **Chart drift from kw:** the env-parity test fails when the kw manifests gain a variable the chart lacks.
- **Kamailio config drift:** the chart's copy of the config must equal the source; the test fails otherwise.
- **kind cannot start on the runner:** the job becomes dispatch-only and the PR says why.

## Acceptance criteria

- [ ] [S-1] `TestChartDefaultRender` passes.
- [ ] [S-1] `TestChartEnvParityWithKw` passes.
- [ ] [S-2] `TestChartNoPlaintextSecrets` passes.
- [ ] [S-3] `TestChartAntiAffinityAndPDBs` passes.
- [ ] [S-4] `TestChartUpgradeOrdering` passes.
- [ ] [S-5] `TestChartStateServices` passes.
- [ ] [S-6] `TestKubernetesGuideCoversTopics` passes.
- [ ] [S-7] `TestChartKamailioConfigMatchesSource` passes.
- [ ] [S-8] `TestKindRollingUpgrade` passes in the kind-upgrade job.
