# incall-ha — implementation plan

Status: draft
Spec: .procoder/specs/incall-ha.md

## Goal

A live call survives the death of its SIP node: a surviving node claims the replicated dialog, takes both legs over, re-points the media to its own relay, and the call completes normally — with a ≤3s audio gap, zero zombies when Valkey survives, and every scenario proven by an automatic failure test.

## Architecture

Every anchored call writes its recovery state (both legs' dialog data + relay state + CDR correlation) to Valkey on every state change and on a 5s heartbeat. When Phase 3 membership marks a node OFFLINE, each surviving node polls for orphaned dialogs and claims them atomically. The taker re-creates both dialog legs from the replicated state, answering and originating in-dialog requests as the old owner (CSeq continuity), while Kamailio's extended failure route sends orphaned in-dialog requests to the taker. Media re-homes by re-INVITing both endpoints to the taker's relay.

## Constraints

- No SIP-path DB calls (Valkey replication is state-change + heartbeat driven).
- Exact CSeq/tag continuity from replicated state; atomic claims; claim released if the owner reappears before the first re-INVITE completes.
- Audio gap ≤3s from ownership claim; the 15s Phase 3 membership bound starts the clock.
- All calls anchor (S-7): the Phase 5 conditional decision is inverted by policy — the reason recorded in traces becomes `policy`.
- Gate per task: gofmt/vet/golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, mutation checks; REVIEW.md applies. CI on the Arc runners; no workloads on the user's Mac.

### Shared contracts (fixed; a stream that must change one asks the lead)

1. **Valkey dialog state** (internal/livestate): `SaveDialogState(ctx, state DialogState, ttl)` / `ClaimDialog(ctx, callId, newNode) (claimed bool, state DialogState, err)` / `ReleaseDialogClaim(ctx, callId)` / `OrphanedDialogs(ctx, offlineNode string) ([]DialogState, error)`. `DialogState{CallID, OwnerNode, Correlation, State (talking|hold|transferring|recording|announcement), Legs [2]DialogLeg, RelayPorts [2]int, UpdatedAt}` with `DialogLeg{CallID, LocalTag, RemoteTag, LocalCSeq uint32, RemoteCSeq uint32, RouteSet []string, Contact, RemoteTarget URI-string, SDP string, Endpoint string (phone AOR), LatchedAddr string}`. Keys: `hello:dialog:{callId}` (TTL 30s), `hello:dialog-claim:{callId}` (no TTL, deleted on release), SCAN `hello:dialog:*` for orphans.
2. **Takeover algorithm (internal/sip):** a taker (any READY node != owner, jittered poll 1–3s) claims, re-creates the relay on its own RTP range, and for each leg builds an in-dialog re-INVITE continuing LocalCSeq+1 from the replicated leg state, From/To/tags preserved, sent via the leg's RouteSet (Kamailio). On 200 from an endpoint the leg's dialog locals are re-learned from the response (tags stay the endpoint's; the taker's local tag is NEW — the endpoint sees the same Call-ID/tags from its side and accepts the re-INVITE; its subsequent requests come back with the new owner's learned data). Both legs re-homed → state talking. Failure on either leg → per spec edge cases.
3. **Kamailio (deploy/kamailio/kamailio.cfg):** in-dialog requests Kamailio cannot forward (dead downstream 408/503) are retried against the OTHER hello-sip node (dispatcher set, skipping the dead one) — the taker answers from replicated state. Additionally `ds_filter`-style: skip the OWNING node for orphaned dialogs once takeover claimed (best effort; the retry alone suffices).
4. **Always-anchor (S-7):** `decideAnchor` returns `policy` for every call; `HELLO_MEDIA_FORCE_ANCHOR` remains as a trace-only no-op; the Phase 5 conditional triggers remain in the trace as informational steps.

## Task 1: Contracts (lead)

Files: `internal/livestate/dialog.go` (contract 1 + tests with real Valkey), config additions (`HELLO_HA_*`, with tests), plan ticks.

- [x] Write dialog state/claim/orphan API + `TestDialogStateLifecycle` (save/claim/conflict/release/orphan-scan/expiry) → pass against Valkey 9.

## Task 2: Replication + takeover (branch phase-7-takeover)

Files: `internal/sip/` (replication hooks in the call lifecycle, takeover poller/claimant, leg re-creation, CDR `ha` flag + zombie counter), `internal/livestate/` (only if a helper is missing), `internal/config/` (HELLO_HA_* only if missing — Task 1 covers), `cmd/hello-sip/main.go` (poller wiring).

- [x] Replication: write-on-change + heartbeat; tests (TestDialogStateLifecycle against real Valkey, TestTakeoverReINVITEs asserts the replicated fields; replication failure non-fatal + counted by hello_dialog_replicated_total{result}).
- [x] Orphan detection + claim + takeover re-INVITEs; tests with in-process phones (TestTakeoverReINVITEs: taker re-INVITEs both, hangup works, trace carries `ha: taken over from …`; TestTakeoverClaim on claim atomicity; TestTakeoverLoopOnValkey runs the loop against real Valkey + membership in CI). The lab's TestKillSIPNodeDuringCall asserts the full takeover end to end.
- [x] Scenario coverage (partial, see report): hold and recording in TestTakeoverScenarioMatrix (in-process); connected/ringing in the lab; blind/attended transfer, announcement and voicemail takeovers are NOT implemented (voicemail is one-legged by design; transfer/announcement state replicates but has no dedicated kill-test).
- [x] Zombie counting + honesty flags (S-6); always-anchor policy flip (S-7); metrics (S-13 list).
- [x] Gate + report per house rules.

## Task 3: Kamailio rerouting (branch phase-7-kamailio, small)

Files: `deploy/kamailio/kamailio.cfg` (+ tests where the shape allows).

- [x] In-dialog failure route: dead downstream → retry other hello node (contract 3). Verified by the lab's TestKamailioInDialogReroute (a callee BYE that reaches the dead node is answered 200 by the taker).
- [x] Gate + report.

## Task 4: Failure suite + rollout (lead)

Files: `test/integration/failure_test.go` upgrades (S-8 matrix, 3s gap measurement), kw manifest (RTP env stays; nothing new cluster-side), docs/ha.md in-call section, digest pinning + Sync rollout, live verification.

- [ ] Upgrade/extend the failure tests; full suite green on CI.
- [ ] Pin digests; Sync to kw; kill a node mid-call on the deployed instance; verify takeover end to end; close stories; report.

## Acceptance criteria

See `.procoder/specs/incall-ha.md` — each criterion cites its named test; the failure suite is the final proof.
