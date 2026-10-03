# ha — implementation plan

Status: draft
Spec: .procoder/specs/ha.md

## Goal

Losing any single hello-sip node, the Valkey primary, PostgreSQL or the control plane does not stop new registrations or calls. Nodes drain without dropping calls. Operators see cluster state. Every guarantee is proven by an automated failure test against the lab, with phones reaching the cluster through Kamailio.

## Architecture

- **Kamailio:** load-balances phones over READY hello-sip nodes (`dispatcher`, with OPTIONS probes) and becomes the edge: Path on REGISTER, Record-Route on INVITE, NAT handling, and the original client address in `X-Hello-Client`.
- **Every Hello node** publishes membership (`internal/cluster`) and runs a lifecycle state machine. Readiness and its OPTIONS answer follow that state, so Kamailio and load balancers see draining and unhealthy nodes.
- **Valkey** is reached through `internal/vkconn`, either one instance or Sentinel.
- **Failure suite:** `test/integration/failure_test.go` injects each failure from the spec into the compose lab.

## Constraints

- HA Levels 1–3 are mandatory; Level 4 is out of scope and never claimed.
- Don't build SIP proxy machinery Kamailio provides (§30). Hello's own Path/flow-token edge stays only for direct-to-node mode.
- No secrets in Kamailio's config or in logs.
- Images are non-root where possible, with exceptions documented. The Kamailio image tag is pinned (`ghcr.io/kamailio/kamailio:6.0.4-bookworm` unless a newer 6.0.x is verified).
- Every task leaves `gofmt`, `go vet`, `golangci-lint` (0), `go test -race ./...` (with test databases) and `procoder check` (0 blocking) clean, and mutation-checks each guarantee (snapshot immediately before, restore immediately after, `cmp`).
- REVIEW.md applies, including the 2026-10-02 and 2026-10-03 additions.
- **Containers:** each stream starts only uniquely named containers and never stops or removes any it didn't create. The lab's host ports (5060, 5062, 5080, 8080–8092) are used by one lab at a time; ask the lead before starting one.

### Shared contracts (fixed; a stream that needs a change asks the lead)

1. **`internal/cluster`**, as committed:
   - `Member`, `State` (Joining, Ready, Draining, Unhealthy, Offline), `Kind`
   - `Store.Publish`, `Leave`, `Members`, `RequestDrain`, `CancelDrain`, `DrainRequested`
   - `TTL` 15s, `TombstoneTTL` 10m
   - Nodes publish every `HELLO_MEMBER_HEARTBEAT` (5s). The member ID is `HELLO_NODE_ID`.
2. **`internal/vkconn.New(ctx, Config, log)`**, as committed, creates every Valkey client in both services. `vkconn.Config` comes from the `config` fields `ValkeyAddr`, `ValkeySentinels` and `ValkeyMaster`.
3. **`internal/config`**, as committed:
   - both services: `ValkeySentinels`, `ValkeyMaster`
   - hello-sip: `TrustedProxies`, `DrainTimeout`, `MemberHeartbeat`
4. **Lifecycle and readiness (hello-sip and hello-control):**
   - **JOINING:** until the node can serve. For hello-sip that means the snapshot is loaded, Valkey answers and the SIP listener is serving; for hello-control, PostgreSQL answers.
   - **READY:** otherwise.
   - **UNHEALTHY:** while a required check fails (the same checks as `/readyz`).
   - **DRAINING:** on SIGTERM or `DrainRequested`. It is cleared by `CancelDrain` only if the node has not started exiting.
   - `/readyz` is 200 only in READY, and its body names the state and reason.
   - Each state change is logged once with its reason.
5. **hello-sip OPTIONS to itself** (the Request-URI host is this node, with no To tag): 200 in READY, 503 with `Retry-After: 5` otherwise. Kamailio's dispatcher probe relies on this.
6. **Draining in hello-sip:**
   - A new initial INVITE or REGISTER gets 503 with `Retry-After: 5`.
   - In-dialog requests, CANCEL and ACK are still handled.
   - Trunk leases are released at once.
   - The node exits when its active calls reach 0, or when `DrainTimeout` passes; at the timeout it sends BYE on both legs, with CDR side `system` and reason `drain timeout`.
   - It publishes `Draining` membership with the live call count.
7. **Kamailio ↔ Hello** (every request Kamailio forwards to a Hello node):
   - **`X-Hello-Client: <ip>:<port>`:** the client's real source after Kamailio's NAT fix-up. Kamailio removes any `X-Hello-Client` arriving from outside.
   - **Sockets:** phones on 5060 (host 5080, advertised as `KAMAILIO_PUBLIC_HOST`:`KAMAILIO_PUBLIC_PORT`, default `127.0.0.1:5080`), Hello on `10.89.53.10:5070`. `Path` and `Record-Route` name the side each party can reach, so a dialog carries two Record-Routes.
   - **REGISTER:** gets `Path: <sip:10.89.53.10:5070;lr;received=...>` (path module, `add_path_received()`), so calls to the phone reach its NAT flow through Kamailio.
   - **Initial INVITE from a phone:** Kamailio record-routes itself (twice, one per socket).
   - **Contacts from phones:** dialog-forming requests and replies get `;alias` (`set_contact_alias`); Kamailio resolves it on requests from Hello (`handle_ruri_alias`).
   - **Keep-alive:** a memory-only `usrloc` list, fed from REGISTER replies, exists solely for `nathelper` OPTIONS pings to phones.
   - **Image:** pinned `ghcr.io/kamailio/kamailio:6.0.8-bookworm` (amd64, emulated on Apple silicon).
   - **Trust:** Hello believes `X-Hello-Client` and stores client `Path` only when the datagram's source IP is in `TrustedProxies`. It uses `X-Hello-Client` as the source for the failed-auth throttle, trunk source validation and binding `Source`. Otherwise it ignores both headers and drops a client Path (the Phase 1 behaviour).
   - **Calls to a phone with a trusted Path:** go through the Path (Route header), in place of Hello's flow-token edge.
   - **Dispatcher:**
     - Set 1 holds every hello-sip node (`sip:10.89.53.11:5060` and `sip:10.89.53.12:5060`, fixed IPs: Kamailio resolves names only via Docker DNS, and an unresolvable entry is fatal at startup).
     - OPTIONS probes every 4s (inactive within 3 × 4s + 1.5s timeout = 13.5s), with `ds_ping_reply_codes` treating only 200 as active and 3 failures marking a node inactive.
     - REGISTER is hashed on the From user, so an AOR sticks to one node while it is active.
     - Other initial requests use round robin over active nodes.
     - A failed relay (timeout or 503) is retried once on the next active node (`ds_next_dst`).
8. **Lab topology:**
   - **Valkey:** `valkey-1` (primary at start), `valkey-2` (replica) and `sentinel-1..3`, master set `hello`, `down-after-milliseconds 3000`, `failover-timeout 10000`. Every Hello service uses `HELLO_VALKEY_SENTINELS`.
   - **Kamailio:** service `kamailio`, host UDP 5080 to 5060, Hello-facing `10.89.53.10:5070`.
   - **Direct ports:** the hello-sip host ports 5060 and 5062 stay for direct-mode tests.
   - **Trusted proxy:** `HELLO_SIP_TRUSTED_PROXIES` is set to Kamailio's address, via a fixed IP on the compose network.

## Task 1: Shared contracts (lead)

Files: `internal/cluster/`, `internal/vkconn/`, `internal/config/` (Sentinel, trusted proxies, drain timeout, heartbeat, and tests), this plan.
Interfaces: contracts 1–3.

- [x] Write `internal/cluster`, and run `HELLO_TEST_VALKEY_ADDR=… go test ./internal/cluster/` → `TestMembersAndTombstones` and `TestDrainRequests` pass.
- [x] Write `internal/vkconn`, and run `go test ./internal/vkconn/` → `TestSingleAndSentinelStartup` passes.
- [x] Extend config, and run `go test ./internal/config/` → `TestLoadValkeyTopology` and `TestLoadTrustedProxies` pass.
- [x] Commit, then create the worktrees `phase-3-sip`, `phase-3-control`, `phase-3-ui` and `phase-3-infra`.

## Task 2: SIP node (branch phase-3-sip)

Files: `internal/sip/`, `internal/snapshot/` (if needed), `internal/livestate/` (only if needed), `cmd/hello-sip/main.go`, a new `internal/lifecycle/` (state machine shared with hello-control; Task 3 reuses it after merge).
Interfaces: contracts 1–7. Produces membership, the OPTIONS behaviour, draining and trusted-proxy handling.

- [x] Build `internal/lifecycle`: a state machine fed by readiness checks, the drain signal and shutdown. It publishes `cluster.Member` on each heartbeat and state change, drives `/readyz` and `hello_node_state`, and logs transitions. Test it with a fake clock and fake checks.
- [x] Wire it into hello-sip with `vkconn.New`, the membership publisher, a drain watcher (`DrainRequested` every 5s) and SIGTERM handling.
- [x] Draining per contract 6, with tests: a new INVITE or REGISTER gets 503 with Retry-After while in-dialog requests work; leases are released; the node exits when calls hit 0; the timeout sends BYE.
- [x] OPTIONS per contract 5, with a test.
- [x] Trusted proxies per contract 7: Path storage, `X-Hello-Client` as the source for throttle, binding and trunk source validation, and calls through a stored Path. Tests: a trusted source is honoured; an untrusted one has its headers ignored and Path dropped; failed auth keys on the client IP.
- [x] Metrics: `hello_node_state`, `hello_drain_active_calls`, `hello_valkey_failovers_total` (count reconnects that follow a primary change, which valkey-go exposes through `Mode` or `Nodes()`; if no event is exposed, compare the primary address on each heartbeat).
- [x] Run the gate and the Phase 1 and 2 lab suite.

## Task 3: Control plane and cluster API (branch phase-3-control)

Files: `internal/api/` (cluster routes, OpenAPI), `internal/store/` (only if an audit helper needs it), `cmd/hello-control/main.go`.
Interfaces: consumes `cluster.Store` and contracts 1–4. Produces `GET /api/v1/cluster`, `GET /api/v1/cluster/nodes`, and `POST` and `DELETE /api/v1/cluster/nodes/{id}/drain`.

- [x] hello-control publishes its own membership (kind control) on a heartbeat through `vkconn`, and leaves cleanly on shutdown. Use a minimal publisher now; once Task 2 merges, switch to `internal/lifecycle`.
- [x] `GET /api/v1/cluster` returns:
  - members, from `cluster.Store.Members`
  - PostgreSQL health
  - Valkey health and mode, plus the Sentinel primary when in Sentinel mode
  - the current configuration revision, and each member's lag
- [x] Drain and undrain: audited (actor, node), a 404 for unknown nodes, and a 409 warning when draining would leave no READY SIP node, unless `?force=true` is passed.
- [x] Add OpenAPI entries; `TestVersionAndOpenAPI` must still route every documented operation. Write `TestClusterAPI`.
- [x] Run the gate.

## Task 4: UI Cluster page (branch phase-3-ui)

Files: `web/src/` (api, `pages/Cluster.tsx`, tests).
Interfaces: the JSON from Task 3. Field names: `members[]` uses `cluster.Member` JSON; `postgres: {up, error?}`; `valkey: {up, mode, primary?}`; `configRevision`.

- [x] Build the Cluster page per spec §21: a node table (ID, kind, state with reason, SIP address, calls, registrations, version, revision lag, heartbeat age), the dependency rows, and the configuration revision. Refresh every 5s. Drain and undrain actions use an in-page confirmation, with a stronger confirmation when the server warns that draining would leave no READY node.
- [x] Write `Cluster.test.tsx` per the spec criterion, mutation-checked.
- [x] Run `pnpm typecheck`, `lint`, `test` and `build`.

## Task 5: Kamailio and Sentinel lab (branch phase-3-infra)

Files: `deploy/kamailio/` (`kamailio.cfg`, `dispatcher.list`), `deploy/valkey/` (sentinel config), `deploy/docker-compose/compose.yaml` (Kamailio, Valkey primary and replica, three sentinels, fixed IPs, env), `Dockerfile` (only if a Kamailio wrapper image is needed).
Interfaces: contracts 7 and 8.

- [x] Write the Kamailio config: dispatcher (contract 7), nathelper keep-alive OPTIONS to phones, path with `add_path_received`, Record-Route, removal of external `X-Hello-Client` and insertion of the trusted one, pike rate limiting, `xhttp_prom` metrics on an internal HTTP port, and logging without credentials.
- [x] Set up Valkey primary, replica and three sentinels. Switch every Hello service to Sentinel env.
- [x] Verify by hand and with a small script under `deploy/`:
  - Phones (`test/sipua`) on host 5080 register and call through Kamailio.
  - Killing `valkey-1` promotes `valkey-2` and Hello recovers.
  - A hello-sip answering 503 to OPTIONS leaves the dispatcher set within 15s.

  Report the evidence.

- [x] Phase 1 and 2 lab tests must still pass. They use direct ports, plus `HELLO_VALKEY_SENTINELS` wherever a test talked to Valkey (`valkey-cli` calls go to the current primary through Sentinel).

## Task 6: Failure suite and docs (lead, branch phase-3-ha)

Files: `test/integration/failure_test.go` (harness and tests), `docs/ha.md`, `README.md`, `test/integration/` (smoke through Kamailio).
Interfaces: consumes everything above.

- [x] Merge the sip, control, ui and infra branches, and run the gate.
- [x] Write the `test/integration/failure_test.go` harness:
  - phones through Kamailio (host 5080)
  - helpers for killing and restarting containers, disconnecting from and reconnecting to a network, and stopping and starting PostgreSQL
  - a wait-until-READY helper using `/api/v1/cluster`
- [x] Write the tests from spec S-10: `TestDrainKeepsCallsAndExits`, `TestKamailioBalancesAndPaths`, `TestKillSIPNodeDuring{Register,Ringing,Call}`, `TestValkeyFailover`, `TestPostgresOutage`, `TestControlPlaneRestart`, `TestPartitionFromValkey` and `TestRollingUpgrade`.
- [x] Write `docs/ha.md`: the architecture, the S-9 dependency-failure table (normative, one row per case), drain and rolling-upgrade procedures, the manual Kamailio-loss procedure, and production guidance (two or more Kamailio behind a VIP or SRV, Sentinel sizing, PostgreSQL HA for Phase 6).
- [x] Extend `TestLabSmoke` to register and call through Kamailio, and update the README (topology, ports, env).
- [x] Run `HELLO_DOCKER=1 go test -timeout 40m ./test/integration/ ./test/integration/failure_test.go` (pass), then the full gate.
