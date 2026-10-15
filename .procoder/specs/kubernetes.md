# kubernetes

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 6 (Kubernetes), §18 (deployment), §30 (engineering rules); milestone `phase-6-kubernetes`. Scope decided by the lead overnight, 2026-10-15 (`.procoder/ask/decisions.md`, "Phase 6 scope"). Builds on `ha` and `incall-ha` (both complete): the cluster already survives node loss and drains with live calls; this spec is how an operator gets that onto a Kubernetes cluster that is not kw.

## Problem

Hello runs on exactly one Kubernetes cluster, kw, from hand-written raw manifests that Kuvryn Sync applies (`deploy/kuvryn-sync/kw/resources.yaml`). Those manifests encode hard-won facts (hostNetwork for SIP and RTP, headless per-node Services, Recreate with a preStop stagger, the Kamailio wait-dispatcher init container, a config-checksum annotation) but are specific to kw's addresses, node names and secrets. Nobody else can install Hello on Kubernetes: there is no chart, no statement of what SIP and RTP need from the network, no guidance on running PostgreSQL and Valkey highly available, and no proof that a rolling upgrade keeps live calls.

## Users

- **Operators of other clusters:** install Hello with `helm install`, point it at their PostgreSQL and Valkey, expose SIP correctly, and upgrade without dropping calls.
- **Evaluators:** install everything, including throw-away PostgreSQL, Valkey and MinIO, with one command to see Hello work.
- **The maintainers:** a change to the manifests that breaks an invariant (anti-affinity, the drain stagger, a secret in plaintext) fails CI.

## In scope

- [S-1] **Helm chart `deploy/helm/hello`.** One chart rendering: hello-control (2 replicas, `migrate up` init container, readiness/liveness, PDB, rolling update), hello-sip (N nodes, each with a stable node identity and a headless per-node Service, probes, resources, PDB, drain-capable termination), Kamailio (the checked-in Kamailio config and a dispatcher list generated from the node count, the `wait-dispatcher` init container, a config-checksum annotation that rolls the pod on a config change, `hostNetwork` option, ports configurable), hello-ui with an optional Ingress, and an optional Ingress for phone provisioning. Env vars, ports, probes and security contexts mirror `deploy/kuvryn-sync/kw/resources.yaml`, which stays the source of truth; kw itself keeps running from Sync and is not changed by this spec.
- [S-2] **Credentials by reference only** (read by `internal/config/config.go`; the kw ones are in `deploy/kuvryn-sync/kw/resources.yaml`). The chart never renders a Secret and never carries a credential in a value: secrets.existingSecret names a Secret holding the database DSN, the application secret key, the SIP nonce secret, the bootstrap admin password and the MinIO credentials. Optional keys (AI, provisioning redirect credentials) are `optional: true` references.
- [S-3] **Placement and disruption.** hello-sip and hello-control carry `podAntiAffinity` (required for hello-sip, whose hostNetwork ports allow one per node; preferred for hello-control) and a PodDisruptionBudget each, so a node drain or an upgrade never removes the last ready SIP node.
- [S-4] **Rolling upgrades that keep calls.** hello-sip upgrades one node at a time, each node draining its calls to a ready survivor before it exits (`terminationGracePeriodSeconds` above `HELLO_DRAIN_TIMEOUT`), and never two nodes at once; Kamailio rolls with Recreate under hostNetwork and waits for dispatcher names to resolve. The ordering is a rendered, tested property, not a convention.
- [S-5] **State services.** Evaluation mode bundles single-instance PostgreSQL, Valkey and MinIO (postgresql.enabled, valkey.enabled, minio.enabled, off by default for production values). Production values point at external services instead: externalPostgresql is just the DSN in the Secret, valkey.external.addr or valkey.external.sentinels and master for Sentinel, minio.external.endpoint. The chart refuses to render when a service is neither bundled nor configured.
- [S-6] **Kubernetes guide `docs/kubernetes.md`.** Installing; SIP networking (hostNetwork versus NodePort or LoadBalancer, what each does to source addresses and why hostNetwork is the recommendation, the RTP port range per node and what must be open, advertised addresses and NAT, `HELLO_SIP_TRUSTED_PROXIES`); anti-affinity and disruption budgets; highly available PostgreSQL (CloudNativePG or a managed service) and Valkey Sentinel guidance; the upgrade procedure and what a caller experiences; how kw differs (Sync, raw manifests).
- [S-7] **Static deployment tests `test/deploy`.** Go tests render the chart with `helm template` for default values and an HA values file and assert the invariants of S-1 to S-5, including env parity with the kw manifests, that the chart's Kamailio config equals deploy/kamailio/kamailio.cfg, and that no rendered object carries a plaintext secret. `helm lint --strict` and `kubeconform` run in CI. The tests skip with a stated reason when `helm` is not on PATH, and fail in CI where it is installed.
- [S-8] **Rolling-upgrade test with active calls.** A CI job creates a kind cluster, installs the chart, registers two phones through Kamailio with the repo's test SIP tools (`test/sipua`), connects a call, runs `helm upgrade` to a changed image tag so every pod rolls, and asserts the call survives (re-homed by the Phase 7 takeover or drain handoff, marked `ha: taken-over` where it moved) and can be hung up from either side, with zero zombies. Where kind cannot run on the CI runners the job is `workflow_dispatch`-only and the reason is stated; the static tests stay in normal CI.

## Out of scope

- Replacing the kw raw manifests with the chart; kw deploys only through Kuvryn Sync.
- An operator, CRDs, or managing PostgreSQL/Valkey replication ourselves: HA state services are an external concern with guidance (S-6).
- Publishing the chart to a registry (OCI/Helm repo) and image publication beyond what `ci.yaml` does.
- Cloud load-balancer specifics, service mesh, and multi-cluster SIP.
- Automated testing of a PostgreSQL or Valkey failover on Kubernetes (covered in the Phase 3 compose lab).

## Constraints

- The chart renders with only `helm template`; nothing in the tests starts a service on a developer machine. The kind job is the only place a cluster exists, in CI.
- Defaults are safe: no credentials in values, no hostNetwork port collisions on a single node (anti-affinity required), pinned images by tag with an immutable-tag recommendation.
- One source for the Kamailio config: the chart carries a copy of deploy/kamailio/kamailio.cfg, enforced equal by a test (same pattern as the kw ConfigMap).
- Gate per task: `go test ./...` for `test/deploy`, `helm lint --strict`, `kubeconform`, `procoder check` 0 blocking; REVIEW.md applies.

## Interfaces

- **Chart values:** global.imageRegistry, global.imageTag, global.sipDomain, secrets.existingSecret, control._, sip.nodes (count), sip.hostNetwork, sip.rtpPortMin/Max, sip.mediaAnchorHost, sip.trustedProxies, kamailio.hostNetwork, kamailio.publicHost, kamailio.publicPort, kamailio.phonesPort, kamailio.service._, ui.ingress._, postgresql.enabled, valkey.enabled, valkey.external._, minio.enabled, minio.external.*.
- **Secret keys:** `dsn`, `secretKey`, `nonceSecret`, `bootstrapPassword`, `minioAccessKey`, `minioSecretKey`; optional AI keys `provider`, `base-url`, `model`, `api-key`.
- **CI:** job kind-upgrade in `.github/workflows/ci.yaml`.

## Data

None. The chart creates no schema; `hello-control migrate up` runs as the init container.

## Edge cases

- One hello-sip node (`sip.nodes: 1`): no survivor to drain to; the chart renders, the guide states calls on it end during an upgrade, and the PDB is omitted so a drain is not blocked forever.
- Fewer cluster nodes than sip.nodes with required anti-affinity: pods stay Pending; the guide says so.
- hostNetwork off: RTP is not reachable through a Service for a port range; sip.mediaAnchorHost then must be a routable address and the guide states the limitation.
- Kamailio dispatcher names not resolving at start (a node mid-upgrade): the wait-dispatcher init container waits, bounded, then starts regardless.

## Open questions

## Failure modes

- **A value missing for a service that is neither bundled nor external:** the chart fails at render time with a message naming the value, never deploys half a system.
- **A hello-sip node that never returns during an upgrade:** the upgrade stalls on that node (rolling update waits for readiness) and the remaining node keeps serving; calls are never drained to a node that is not ready.
- **Kamailio restarting during an upgrade:** in-dialog signalling of calls it record-routed is interrupted until it is back (stated in the guide; Kamailio redundancy is the Phase 7 documented limitation).
- **kind unavailable on the runners:** the upgrade job is dispatch-only, the static tests still gate every pull request.

## Acceptance criteria

- [ ] [S-1] `TestChartDefaultRender` passes: fails if the default render lacks any of hello-control, hello-sip, Kamailio or hello-ui, or hello-control lacks two replicas and the migrate init container.
- [ ] [S-1] `TestChartEnvParityWithKw` passes: fails if a hello-sip, hello-control or Kamailio environment variable in the kw manifests is missing from the chart's render (other than a documented kw-only one).
- [ ] [S-2] `TestChartNoPlaintextSecrets` passes: fails if any rendered object is a Secret or any credential-named env var carries a literal value instead of a secretKeyRef.
- [ ] [S-3] `TestChartAntiAffinityAndPDBs` passes: fails if hello-sip lacks required anti-affinity on the hostname topology or hello-sip and hello-control lack a PodDisruptionBudget in the HA render.
- [ ] [S-4] `TestChartUpgradeOrdering` passes: fails if hello-sip can update more than one node at a time, its grace period does not exceed the drain timeout, or Kamailio lacks Recreate and the wait-dispatcher init container under hostNetwork.
- [ ] [S-5] `TestChartStateServices` passes: fails if the default render omits the bundled services, the production render still bundles them, or a missing external value renders instead of failing.
- [ ] [S-6] `TestKubernetesGuideCoversTopics` passes: fails if docs/kubernetes.md lacks any of hostNetwork, NodePort, the RTP range, anti-affinity, CloudNativePG, Sentinel or the upgrade procedure.
- [ ] [S-7] `TestChartKamailioConfigMatchesSource` passes: fails if the chart's Kamailio config differs from deploy/kamailio/kamailio.cfg; `TestChartLint` and `TestChartKubeconform` (test/deploy) pass: fail if helm lint --strict or kubeconform rejects either render.
- [ ] [S-8] `TestKindRollingUpgrade` in test/kindupgrade, run by the kind-upgrade job in .github/workflows/ci.yaml, passes: fails if the call placed before helm upgrade is lost, cannot be hung up, or leaves a zombie, or (if kind cannot run) the PR states exactly why the job is dispatch-only.

<!-- Decided overnight, recorded in decisions.md: hello-sip as a StatefulSet; separate Services per node by pod name; Secret layout; kind job on the Arc runners with a workflow_dispatch fallback. -->
