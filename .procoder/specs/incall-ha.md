# incall-ha

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 7 (Advanced HA), §17.4 (HA Level 4), §30 (engineering rules). Decided 2026-10-05 (`.procoder/ask/answers.md`): **anchor everything** — every call anchors through a hello-sip relay, reversing the Phase 5 conditional policy deliberately — and **in-call HA applies to all calls**: when the node owning a live call dies, a surviving node takes the dialog over and the call survives.

## Problem

Phase 3 made everything around a call survive a node's death; the call itself did not. When the hello-sip node owning a live call dies, both endpoints lose their signaling peer: audio continues briefly (both endpoints still send RTP into the void), but hangup, hold, transfer and CDR closure are lost — the call goes zombie. With the Phase 7 decision, every call is anchored, which makes the media and dialog state known to the cluster and recoverable: a surviving node can take the dialog over, re-point both endpoints' media to its own relay, and the call — including recording and CDR — continues.

Per spec §17.4: "Do not claim seamless in-call HA until specific failure scenarios are implemented and automatically tested." This spec defines those scenarios; the tests exist before the claim.

## Users

- **Phone users:** a node dying mid-call costs them at most a few seconds of audio gap; they stay in the call and can hang up normally. Phones on surviving capacity never notice beyond that gap.
- **Operators:** in-call survival is a stated, tested property per call (a per-call flag in the CDR and live view); zombie calls (signaling lost, no recovery) are counted, never silent.

## In scope

- [S-1] **Dialog replication:** the node owning a call continuously writes the call's recovery state to Valkey (`hello:dialog:{callId}`, heartbeat 1s, TTL 10s — amended 2026-10-06 from ≤5s/30s): both legs' full dialog data (Call-ID, local/remote tags, local/remote CSeq, route sets, contacts, remote target URIs, negotiated SDP per leg), the media relay state (allocated ports, per-leg latched addresses), recording/announcement state, and CDR correlation. Written off the SIP transaction path (state-change + heartbeat driven, like Phase 3 membership).
- [S-2] **Ownership and takeover:** each dialog records its owning node. Surviving nodes watch for owned dialogs whose owner went OFFLINE (Phase 3 membership expiry); the first survivor claims the dialog atomically (Valkey claim, `conflictPolicy: fail` semantics), and re-homes it: the new owner re-creates both dialog legs FROM THE REPLICATED STATE on its own node — new relay ports, new local tag/CSeq continuity from the replicated counters — and re-INVITEs each endpoint so both dialogs continue with the endpoints' tags preserved but the B2BUA side taken over by the survivor. Endpoints route these via Kamailio (alive); Kamailio forwards in-dialog requests to the new owner because the owner re-registers its claim onto the dialog's route path (see S-3).
- [S-3] **Kamailio in-dialog rerouting:** phones' dialogs route through Kamailio (their Record-Route). When Hello's node dies, Kamailio's forwarding to the dead node fails (408/503). Kamailio's failure route is extended: on a failed in-dialog forward to a dead Hello node, query the cluster API (or dispatcher state) for the dialog's new owner and retry there — the new owner answers with replicated dialog state, so the endpoint never sees the node change. Kamailio config ships this (dispatcher → hello nodes with the Phase 3 `ds_*` failover extended by an in-dialog retry to the surviving set).
- [S-4] **Media re-homing:** on takeover the new owner allocates fresh relay ports and re-INVITEs both endpoints to them; each endpoint's ACK completes the new media path. Audio gap: the takeover itself (claim to both endpoints re-homed) within 3 seconds; from node death to restored audio, the detection time plus that — a graceful handoff milliseconds, a crash about 4–4.5 s with the 1 s/4 s membership timing (lab: re-homed 3.5–4.1 s after the kill, asserted ≤6 s), a crash restarted in place about 4 s plus the restart (measured by the failure tests, amended 2026-10-06). Recording (if active) continues on the new owner under the same correlation id; announcements in progress restart from their beginning.
- [S-5] **Scenario matrix — all automatic tests (spec §17.4 list):** node death during connected call; during hold; during a blind transfer; during an attended transfer (either the transfer half or the bridged half); during recording; during an announcement; during a voicemail recording; node death during ringing (already Phase 3, re-verified under always-anchor); simultaneous death of both the call's node and Kamailio is explicitly **out of scope** (Kamailio redundancy is documented, not tested).
- [S-6] **Honesty in the product:** the CDR and live view mark each call `ha: taken-over` when it survived a takeover; a zombie (no recovery possible — e.g. Valkey also lost) is counted in `hello_zombie_calls_total`; docs state the guarantee exactly: "a live call survives the death of its SIP node, when the cluster retains Valkey and at least one Kamailio", with the measured audio gap per failure type (graceful handoff, crash restarted in place, crash with the node down) and the CDR trace recording each call's takeover time and media gap.
- [S-7] **Always-anchor policy:** the anchoring decision (Phase 5) defaults to anchor-for-all-calls (the conditional logic remains for traces: the reason becomes `policy`); `HELLO_MEDIA_FORCE_ANCHOR` stays as a no-op-with-trace for compatibility. The bandwidth/latency trade-off was accepted explicitly (decisions.md 2026-10-05).
- [S-8] **Failure-injection suite extension:** the Phase 3 suite's kill-during-call test is upgraded from "audio continues, signaling lost" to the full takeover assertion: both endpoints re-INVITEd to the survivor within 3s of ownership claim and within 6s of the kill, call completable and hangup-able afterwards, CDR closed with `ha: taken-over`, zero zombies. Plus kill-during-hold, kill-during-transfer, kill-during-recording, and a double-failure (node dies while another node is mid-takeover of a different call).

## Out of scope

- Kamailio redundancy/HA (documented in `docs/ha.md`; a dead Kamailio still breaks in-dialog signaling of calls it record-routed — stated).
- Surviving the loss of Valkey AND the node simultaneously (replicated state is gone; zombies counted, per S-6).
- Zero-gap audio (the measured gaps of S-4 are the guarantee; no seamless splice).
- Stateful recording splice across takeover (the recording pauses at node death and continues on the new owner as a second MinIO object linked to the same correlation id).
- Transcoding, SRTP, conferencing (unchanged).

## Constraints

- Dialog replication is off the SIP transaction path (state-change + heartbeat writes, Phase 3 discipline); a replication write failure is logged and counted, never fatal to the call — but an unreplicated call that loses its node becomes a zombie (counted, honest).
- Takeover claims are atomic across nodes (Valkey Lua, `conflictPolicy: fail` semantics); two survivors must never both take the same dialog.
- CSeq continuity from replicated state is exact — off-by-one breaks the endpoint's dialog.
- New dependencies: none planned.
- Gate per task: gofmt/vet/golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, mutation checks; REVIEW.md applies. CI on the Arc runners; deployment via Sync; no workloads on the user's Mac.

## Interfaces

- **Valkey:** `hello:dialog:{callId}` (replicated state JSON, TTL 10s, heartbeat 1s; 30s/5s before the 2026-10-06 amendment), `hello:dialog-claim:{callId}` (takeover claim, atomic Lua), owner id inside the state; SCAN pattern for orphan detection.
- **Env:** `HELLO_HA_TAKEOVER_ENABLED` (default true), `HELLO_HA_TAKEOVER_POLL` (default 1s), `HELLO_HA_TAKEOVER_JITTER` (random 0–500ms to avoid thundering herd; was 0–2s before the 2026-10-06 amendment).
- **HTTP:** `GET /api/v1/calls` gains `ha` state per call (`owned | taken-over`); `hello_dialog_replicated_total`, `hello_dialog_takeovers_total`, `hello_zombie_calls_total` metrics.
- **Kamailio:** failure-route extension shipped in `deploy/kamailio/kamailio.cfg` (S-3).

## Data

- **Valkey:** dialog replication keys per S-1 (TTL 10s), claim keys, tombstones unchanged from Phase 3.
- **PostgreSQL:** none new — CDRs gain nothing schema-wise (`ha` flag lives in the trace JSON).
- **MinIO:** unchanged (recordings continue under the same correlation id, second object on takeover).

## Edge cases

- Two nodes die at once, each owning calls: survivors claim disjoint dialog sets (atomic claims guarantee it).
- The call's two legs are owned by DIFFERENT nodes (Phase 1 forks are same-node, but a transfer can create cross-node legs): each leg's owner is recorded; takeover of one leg re-homes it to the taker; cross-node bridging is preserved via the replicated peer-leg pointer.
- Takeover starts while the dead node's membership record is still fresh (race with a node that is actually alive but slow): the claim is released if the owner heartbeat reappears before the first re-INVITE completes.
- An endpoint does not answer the takeover re-INVITE (phone lost power too): the leg fails, the other leg gets a normal BYE/CDR — a one-sided zombie is still avoided.
- A call mid-announcement or mid-voicemail-prompt: the prompt restarts from the beginning (S-4), recording continues after.
- Recording split across takeover: two MinIO objects, same correlation id; the UI concatenates playback or plays both parts sequentially.

## Failure modes

- **Valkey unavailable:** takeover cannot work (state is gone) — zombies counted; everything else degrades as in Phase 3. Honest per S-6.
- **No surviving hello-sip node:** nothing to take over to; zombies counted.
- **Takeover re-INVITE rejected by an endpoint** (488, 603): that leg ends with BYE, the other leg gets CDR closure — never a zombie.
- **Claim lost mid-takeover** (network partition heals): the original owner reappearing detects the claim and yields (its dialogs were re-homed); double-ownership is prevented by the atomic claim.

## Acceptance criteria

- [ ] [S-1] `TestDialogReplication` passes — fails if the replicated state on Valkey is missing any field needed to re-create a leg (Call-ID, tags, CSeq, routes, contacts, SDP, relay ports, correlation), or the heartbeat does not refresh the TTL.
- [ ] [S-2] `TestTakeoverClaim` passes — fails if two survivors can both claim the same orphaned dialog, or a claim is not released when the original owner reappears pre-takeover.
- [ ] [S-3] `TestKamailioInDialogReroute` passes — fails if an in-dialog request to a dead Hello node is not rerouted to the new owner and answered from replicated state.
- [ ] [S-4] `TestTakeoverMediaGap` passes — fails if the audio gap from node death to restored media exceeds 3s, or the endpoints' media does not land on the new owner's relay.
- [ ] [S-5] `TestTakeoverScenarioMatrix` passes — fails if any scenario in the S-5 matrix (connected, hold, blind transfer, attended transfer, recording, announcement, voicemail recording, ringing) does not survive per its definition.
- [ ] [S-6] `TestHonestyFlags` passes — fails if a taken-over call is not marked in CDR/live view, or a zombie is not counted in `hello_zombie_calls_total`.
- [ ] [S-7] `TestAlwaysAnchorPolicy` passes — fails if a plain LAN-to-LAN call does not anchor after this phase.
- [ ] [S-8] `TestDoubleFailure` passes — fails if a node dying while another node mid-takeover corrupts or zombies the takeover calls.
- [ ] [S-8] The Phase 3 `TestKillSIPNodeDuringCall` suite is upgraded: the old "audio continues, signaling lost" assertion is replaced by full takeover assertions.
- [ ] Metrics/UI: `hello_dialog_takeovers_total`, `hello_zombie_calls_total`, per-call `ha` flag — `TestHAMetrics` extension fails if they do not move.
- [ ] Docs: `docs/ha.md` gains the in-call HA guarantee, stated exactly per S-6, with the scenario matrix and the two named limitations (Kamailio, Valkey+node double loss) — fails if a later `procoder docs` check finds the docs guarantee diverging from the S-5 matrix or missing either limitation.

## Amendments

- 2026-10-06 (kw live evidence): a crashed node restarted in place under the same node ID within the (then 15 s) OFFLINE window never went OFFLINE, so S-2's OFFLINE trigger alone lost its calls. Every process now has an incarnation id (membership `incarnation`, dialog record `ownerIncarnation`); records of a dead incarnation are claimed at once by any READY node, the restarted one included, and an in-dialog request that misses on a node is checked against the replicated record and takes the call over on demand instead of 481 (`docs/ha.md` "Restart in place"). S-4's gap is reported honestly: the CDR trace gives the takeover time from the claim (the ≤3 s target) and the media gap from the owner's last replication write, which includes detection time. S-6's CDR is one per logical call, written by the node that ends it, with the original start, routing and the side that hung up; a yielding node writes none and leaves the live record to the taker.
- 2026-10-06 (user decision, faster crash detection): membership heartbeat 1 s and TTL 4 s (heartbeat at most TTL/3); dialog replication scaled with it (1 s heartbeat, 2 s owner-freshness window, 10 s record TTL) and the takeover jitter cut to 0–500 ms. The false "≤3 s from node death" claim is replaced by the measured gaps per failure type in S-4; the zombie reaper counts a dead node's connected calls, not its ringing ones.

## Open questions

<!-- The recovery model (Kamailio in-dialog rerouting + replicated-state continuation) is the design this spec commits to; alternatives (endpoint re-establishment with new dialogs) were rejected for a visible call drop. Remaining detail — exact Valkey state schema — is the plan's Task 1. -->
