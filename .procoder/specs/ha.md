# ha

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 3 (HA), §17 (high availability), §18 (SIP traffic distribution), §21 (cluster page) and §27 (failure injection). Decided 2026-10-02/03 (`.procoder/ask/answers.md`):

- **Load balancer:** phones reach the cluster through one SIP address. Kamailio's `dispatcher` module is a production SIP-aware balancer shipped with Hello (config, image wiring, docs), not a lab tool.
- **Valkey:** high availability through Valkey Sentinel (a primary, a replica and three sentinels), with automated failover tests.
- **PostgreSQL:** outage and restart are tested (HA Level 1). Real PostgreSQL HA is Phase 6 deployment guidance.

## Problem

Hello runs two active SIP nodes, but its high availability is assumed, not proven or operable:

- **Phones** are configured with one node's address, so they lose service when that node dies.
- **Planned maintenance** kills calls, because nodes cannot be drained.
- **Valkey** is a single point of failure.
- **Operators** cannot see cluster state.
- **No HA promise** has an automated failure test.

Spec §17 makes HA Levels 1–3 mandatory for the first production-ready release. Phase 3 delivers them, with each guarantee proven by a failure-injection test (§27, "every HA promise must have an automated failure test").

## Users

- **Operators** need to:
  - see every node's state, load, version and configuration revision
  - drain a node for maintenance without dropping calls
  - roll upgrades through the cluster
  - trust that losing one node or dependency degrades service predictably instead of stopping it
- **Phone users:** their phones point at one SIP address and keep working when any single SIP node, Valkey node or the control plane fails.
- **Developers** need to run the whole failure suite against the lab with one command.

## In scope

- [S-1] **Node membership.** Every hello-sip and hello-control node publishes a membership record in Valkey (`hello:member:{nodeId}`) with:
  - ID and kind
  - advertised SIP and HTTP addresses
  - transports
  - lifecycle state
  - active calls and registrations
  - software version
  - configuration revision
  - start time and last heartbeat

  The record is refreshed every 5s and expires after 15s. A record that expires is reported `OFFLINE`, as a tombstone, for 10 minutes.

- [S-2] **Lifecycle states** (`JOINING`, `READY`, `DRAINING`, `UNHEALTHY`, `OFFLINE`), with these transitions:
  - JOINING until the snapshot is loaded, Valkey is reachable and the SIP listener is serving.
  - READY otherwise.
  - UNHEALTHY while a required dependency fails.
  - DRAINING on SIGTERM or a drain request.

  `/readyz` is 200 only in READY.

- [S-3] **Graceful draining** (§17.7):
  - The node marks itself DRAINING and fails readiness.
  - New initial INVITEs and REGISTERs get 503 with `Retry-After`, so phones and the balancer move them to another node.
  - Trunk leases are released at once, so another node takes over trunk registration and health checks.
  - Existing dialogs continue. In-dialog requests are still served.
  - The node exits when its active calls reach zero, or when `HELLO_DRAIN_TIMEOUT` (default 2h) passes, whichever comes first. On timeout it ends its remaining calls with BYE, CDR side `system`, reason `drain timeout`.
- [S-4] **Drain API:** `POST /api/v1/cluster/nodes/{id}/drain` and `DELETE /api/v1/cluster/nodes/{id}/drain` (cancel) write a drain request to Valkey. The node acts on it within 5s, and the action is audited.
- [S-5] **Cluster view.**
  - `GET /api/v1/cluster` returns the members, PostgreSQL and Valkey health (including Sentinel's current primary), the configuration revision, and each node's revision lag.
  - The UI Cluster page lays this out per §21, with drain and undrain actions.
- [S-6] **Kamailio SIP balancer.** `deploy/kamailio/` ships a Kamailio 6.0 configuration and compose service. Kamailio:
  - Load-balances phones across READY hello-sip nodes with `dispatcher`, probing each node with OPTIONS every 5s; a node answering 503 (draining) or nothing is out of rotation within 15s.
  - Keeps REGISTER of the same AOR on the same node while it is healthy (hashing the From user).
  - Inserts `Path` (RFC 3327) on REGISTER, using `nathelper` for NAT keep-alive and received-address handling, so any Hello node reaches any phone through Kamailio.
  - Record-routes initial INVITEs, so in-dialog requests from either side cross it.
  - Rate-limits REGISTER and INVITE per source IP (`pike`).
  - Is stateless apart from its dispatcher view.
- [S-7] **Hello behind a trusted edge.**
  - **Config:** `HELLO_SIP_TRUSTED_PROXIES` (CIDRs) lists the balancers. Only requests from them may carry a `Path` header, which the registrar then stores, and only they are trusted for source-IP and `received` information, through a `Via` they added.
  - **Throttling:** failed-auth throttling and trunk source validation use the original client address that Kamailio forwards (`X-Hello-Client`, set by Kamailio and stripped from untrusted sources). Without it, every phone would share Kamailio's IP.
  - **Calls:** reach phones through the stored Path.
  - **Direct mode:** without trusted proxies, the Phase 1 Path/flow-token edge routing stays as the direct-to-node mode.
- [S-8] **Valkey Sentinel.**
  - **Connection:** with `HELLO_VALKEY_SENTINELS` (host:port list) and `HELLO_VALKEY_MASTER` set, both services connect through Sentinel and follow failover. `HELLO_VALKEY_ADDR` stays for a single Valkey.
  - **Lab:** a primary, a replica and three sentinels.
  - **During a failover:** state operations get the existing 503 and `Retry-After` handling. After it, nodes are READY again within 15s.
  - **Async replication:** writes acknowledged just before the failover may be lost. The spec accepts this, and phones restore lost bindings on their next refresh.
- [S-9] **Dependency-failure behaviour table** (§17.8), implemented and documented in `docs/ha.md`, one row per case:
  - PostgreSQL unavailable, and PostgreSQL restart
  - Valkey primary loss (Sentinel failover), and all of Valkey unavailable
  - a SIP node isolated from Valkey (network partition)
  - stale configuration (revision lag)
  - duplicate registration updates
  - delayed or out-of-order NOTIFY
  - Kamailio loss
  - hello-control loss

  Each row states what is detected, what the node reports (state, readiness, metrics), what still works, and how it recovers.

- [S-10] **Failure-injection suite.** `test/integration/failure_test.go` (`HELLO_DOCKER=1`) automates each case against the lab through Kamailio:
  - kill a SIP node during REGISTER, during ringing, and during a connected call
  - kill and restart the control plane
  - a PostgreSQL outage and restart
  - Valkey primary failover
  - a network partition of one SIP node from Valkey
  - a rolling upgrade of both SIP nodes with a call active
  - graceful draining
- [S-11] **Metrics:**
  - `hello_node_state{state}`, a one-hot gauge
  - `hello_cluster_members{kind,state}`
  - `hello_config_revision_lag`
  - `hello_drain_active_calls`
  - `hello_valkey_failovers_total`
  - the Kamailio dispatcher state, exported through Kamailio's `xhttp_prom`
- [S-12] **Docs and lab:** `docs/ha.md` covers the architecture, the failure table, how to drain and roll upgrades, and production guidance: two or more Kamailio behind a VIP (keepalived) or DNS SRV, Sentinel sizing, and PostgreSQL HA options for Phase 6. The compose lab starts Kamailio, both SIP nodes and Sentinel Valkey with one command. Phones use Kamailio's address (host UDP 5080).

## Out of scope

- HA Level 4: an established call surviving loss of the node that owns its dialog. That is Phase 7. A connected call whose node dies keeps its RTP, because media is direct, but loses signalling. That is a stated and tested limitation, never described as seamless.
- PostgreSQL replication and promotion (Phase 6 guidance).
- Kamailio redundancy inside the compose lab. The lab's single Kamailio is documented as a single point of failure, and production runs two or more behind a VIP or DNS SRV (`docs/ha.md`). The "load balancer loss" test from §27 is a documented manual procedure, not an automated lab test.
- TLS/TCP, WebRTC and media anchoring (later phases).
- Kubernetes manifests (Phase 6).
- Autoscaling.

## Constraints

- **Required availability:** HA Levels 1–3 (§17.1–17.3). Losing any single hello-sip node, the Valkey primary, PostgreSQL or the control plane must not stop new registrations or new calls through the survivors. A short window (≤15s) of 503 with `Retry-After` is allowed during a Valkey failover.
- **Failure tests:** every guarantee in the dependency-failure table has an automated test in S-10, except Kamailio loss (manual, documented).
- **SIP machinery:** don't build custom SIP proxy machinery where Kamailio meets the need (§30). Hello's own edge routing stays only for direct-to-node deployments.
- **Determinism:** state transitions are logged once, with the reason. Hidden "smart" failover is avoided (§30).
- **Secrets:** no new secrets in logs. Kamailio's config carries no Hello secrets.
- **Images:** pinned Kamailio image tag. Images and containers run as non-root where the image allows; Kamailio's exception, if any, is documented.

## Interfaces

- **Env:**
  - **Both services:** `HELLO_VALKEY_SENTINELS`, `HELLO_VALKEY_MASTER`.
  - **hello-sip:** `HELLO_SIP_TRUSTED_PROXIES`, `HELLO_DRAIN_TIMEOUT` (2h), `HELLO_MEMBER_HEARTBEAT` (5s).
- **HTTP:**
  - `GET /api/v1/cluster`
  - `GET /api/v1/cluster/nodes`
  - `POST /api/v1/cluster/nodes/{id}/drain`
  - `DELETE /api/v1/cluster/nodes/{id}/drain`
- **SIP:** Kamailio on UDP 5080 (host) and 5060 (inside the network). The headers between Kamailio and Hello are `Path`, `Record-Route` and `X-Hello-Client`.
- **UI:** a Cluster page at `/cluster` with drain actions.
- **Files:** `deploy/kamailio/kamailio.cfg` (with its dispatcher list or database), `docs/ha.md`, `test/integration/failure_test.go`.

## Data

- **Valkey:**
  - `hello:member:{nodeId}`: membership record (TTL 15s)
  - `hello:member:tomb:{nodeId}`: OFFLINE tombstone (10 minutes)
  - `hello:drain:{nodeId}`: drain request
- **PostgreSQL:** none new. Drain requests are audited in `audit_events`.
- **Kamailio:** the dispatcher set is generated from the compose service names. Phase 6 replaces it with a dynamic source.

## Edge cases

- **Restart reuses a node ID:** the old tombstone is cleared and the record starts JOINING.
- **Drain requested twice, or cancelled while calls are still active:** the node returns to READY only once its dependencies are healthy.
- **Drain timeout during a call's re-INVITE:** the BYE follows the re-INVITE transaction's end.
- **Kamailio removes a node while it has live calls** (draining): in-dialog requests still reach it through Record-Route.
- **A phone re-REGISTERs through Kamailio to a different node after a failover:** the binding is overwritten (same contact), not duplicated. The Path then points through Kamailio, so any node reaches the phone.
- **Sentinel failover while a REGISTER is in flight:** the phone gets 503 with `Retry-After` and retries. No binding is left without a TTL.
- **Both SIP nodes DRAINING at once:** Kamailio has no destination, and new calls get 503 from Kamailio. The API warns when a drain request would leave no READY node.
- **Network partition:**
  - **Isolated node:** a SIP node cut off from Valkey goes UNHEALTHY and keeps its established dialogs, but refuses new work.
  - **Membership:** the other node sees its membership record expire.
  - **Recovery:** on reconnect it returns to READY.

## Failure modes

The `docs/ha.md` table is normative. Its key rows:

- **SIP node dies:**
  - Kamailio's OPTIONS probe drops it within 15s.
  - New REGISTERs and INVITEs go to the survivor.
  - Phones whose bindings were stored through it stay reachable: the Path points through Kamailio, and their bindings live in Valkey.
  - Its calls lose signalling (Level 4 is out of scope) and disappear from the live view within their 30s TTL.
- **Valkey primary dies:**
  - Sentinel promotes the replica (in the lab, under 10s with `down-after-milliseconds` 3000), and Hello reconnects.
  - Until then, state operations get 503 and nodes go UNHEALTHY.
  - Within 15s of promotion, nodes are READY again.
- **PostgreSQL down:**
  - SIP keeps registering and calling from its snapshot.
  - CDRs buffer, with drops counted.
  - hello-control readiness fails.
  - After restart, snapshots resume and buffered CDRs flush.
- **hello-control down:** telephony is unaffected. The management API and UI are unavailable until a replica is back.
- **Kamailio down (lab):** new SIP traffic stops. In production the VIP or SRV moves to the second Kamailio (documented, manual test).

## Acceptance criteria

- [ ] [S-1] [S-2] `TestMembershipLifecycle` in `internal/lifecycle` (Valkey) passes. It fails if a node is not reported JOINING, then READY, then DRAINING, then OFFLINE with the right timings, if `/readyz` is 200 outside READY, or if an expired node is not tombstoned.
- [ ] [S-3] `TestDrainKeepsCallsAndExits` in `test/integration` (failure_test.go) passes. It fails if, after a drain request, the draining node accepts a new INVITE, drops the active call, keeps its trunk leases, or exits before that call ends; or if, with `HELLO_DRAIN_TIMEOUT` short, it does not BYE the remaining call and exit.
- [ ] [S-4] [S-5] `TestClusterAPI` in `internal/api` passes. It fails if `/api/v1/cluster` omits a member, state, load, version, revision lag, or dependency health, if a drain request is not audited, or if draining the last READY node is not warned.
- [ ] [S-6] [S-7] `TestKamailioBalancesAndPaths` in `test/integration` (failure_test.go) passes. It fails if phones registering through Kamailio are not spread over both nodes, if a call between phones registered on different nodes does not connect, or if failed-auth throttling keys on Kamailio's IP instead of the client's.
- [ ] [S-10] `TestKillSIPNodeDuringRegister`, `TestKillSIPNodeDuringRinging` and `TestKillSIPNodeDuringCall` in `test/integration` (failure_test.go) pass. Each fails if, after the kill, a new REGISTER and a new call through Kamailio do not succeed within 20s. They also fail if a ringing caller is left hanging rather than getting a final response or timing out, or if the dead node's call is still listed after 40s.
- [ ] [S-8] [S-10] `TestValkeyFailover` in `test/integration` (failure_test.go) passes. It fails if, after the Valkey primary is killed, nodes are not READY within 15s of promotion, if new registrations and calls do not succeed, or if any binding lacks a TTL.
- [ ] [S-9] [S-10] `TestPostgresOutage` and `TestControlPlaneRestart` in `test/integration` (failure_test.go) pass. They fail if registration or calling stops while PostgreSQL or every hello-control is down, or if CDRs written during the outage are not in PostgreSQL after it returns.
- [ ] [S-9] [S-10] `TestPartitionFromValkey` in `test/integration` (failure_test.go) passes. It fails if a SIP node disconnected from the Valkey network is not UNHEALTHY within 15s, keeps receiving new calls from Kamailio, or does not return to READY within 15s after reconnecting.
- [ ] [S-3] [S-10] `TestRollingUpgrade` in `test/integration` (failure_test.go) passes. It fails if draining and restarting each SIP node in turn, with one long call active, drops that call or makes any new registration or call fail.
- [ ] [S-11] `TestHAMetrics` in `internal/lifecycle` and `TestClusterMetrics` in `internal/api` pass. They fail if the node-state, drain, failover, member and revision-lag metrics do not move through a JOINING → READY → DRAINING cycle.
- [ ] [S-5] `procoder test` and `procoder lint` pass over `web/`. `Cluster.test.tsx` fails if a node's state, load, version or revision lag is not shown, or if draining the last READY node doesn't ask for confirmation.
- [ ] [S-12] `TestLabSmoke` (`HELLO_DOCKER=1`) registers phones and completes a call through Kamailio on host UDP 5080, failing otherwise. `docs/ha.md` contains every row of the S-9 table.

## Open questions

<!-- All resolved 2026-10-02/03; answers in .procoder/ask/answers.md and folded into Source, In scope and Out of scope. -->
