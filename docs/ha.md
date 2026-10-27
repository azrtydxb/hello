# High availability

Hello keeps taking registrations and calls when any single SIP node, the Valkey
primary, PostgreSQL or the control plane fails. This page explains how, says
exactly what each failure does, and shows how to drain nodes and roll upgrades.
Every row of the failure table is proven by an automated test in
`test/integration/failure_test.go`, except where a row says otherwise (the
Kamailio row is proven on a kind cluster, by `test/kindha`).

## What Hello guarantees

| Level | Guarantee                                                                  | Status     |
| ----- | -------------------------------------------------------------------------- | ---------- |
| 1     | Losing hello-control or the UI does not interrupt telephony                | Guaranteed |
| 2     | When a SIP node fails, phones register through another node                | Guaranteed |
| 3     | When a SIP node fails, new internal, inbound and outbound calls still work | Guaranteed |
| 4     | An established call survives loss of the node controlling it               | Guaranteed |

Level 4 (Phase 7, in-call HA): **a live call survives the death of its SIP
node, when the cluster retains Valkey and at least one Kamailio.** The audio
gap depends on how the node goes (lab measurements, `test/integration`):

| How the node goes                        | Noticed by                                        | Audio gap                                                                                                                    |
| ---------------------------------------- | ------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Graceful handoff (drain, SIGTERM)        | The handoff mark, at once                         | The re-INVITE round trip: milliseconds (the node relays until then)                                                          |
| Crash, restarted in place (same node ID) | The new process's incarnation                     | About 3–4 s (lab: re-homed 2.7–3.8 s after the kill), plus however long the restart takes                                    |
| Crash, node stays down                   | Membership OFFLINE (≤4 s: 1 s heartbeat, 4 s TTL) | About 3.5–4.5 s (lab, two runs: OFFLINE 3.3–3.6 s and re-homed 2.8–4.6 s after the kill; media gap 3.4–4.6 s); asserted ≤6 s |

A crash is noticed when membership marks the node OFFLINE, or, when it is
restarted in place under the same node ID before that, as soon as its new
process is up — see [Restart in place](#restart-in-place). The takeover
itself, from the claim to both endpoints re-homed, takes milliseconds in
the lab (the ≤3 s target bounds it). The call is taken over by a
surviving node (or the restarted node itself) from its replicated dialog
state, both endpoints are re-INVITEd to the taker's media relay, and the
call can be held, recorded and hung up as before (a transfer requested
after the takeover is refused; see below). The CDR trace and the live view
(`GET /api/v1/calls` field `ha`, a "Taken over" badge on the Active Calls
page) mark such a call `taken-over`, and the trace records the real media
gap (see [What the CDR says](#what-the-cdr-says)); a call no survivor could
save (see the two limitations below) is counted in
`hello_zombie_calls_total` and never silently dropped.

## Architecture

```text
             phones / softphones                 SIP trunks
                     |                               |
              +------v------+                        |
              |  Kamailio   |  SIP-aware balancer:   |
              | (dispatcher)|  OPTIONS health, Path, |
              +--+-------+--+  Record-Route, NAT     |
                 |       |                           |
        +--------v-+   +-v--------+                  |
        |hello-sip-1|  |hello-sip-2|<-----------------+
        +-----+-----+  +-----+-----+
              |   shared state   |
        +-----v------------------v-----+     +--------------------+
        | Valkey primary + replica,    |     | PostgreSQL         |
        | three Sentinels              |     | (configuration,    |
        | (bindings, calls, trunks,    |     |  CDRs)             |
        |  membership)                 |     +---------^----------+
        +------------------------------+               |
                                             +---------+----------+
                                             | hello-control x2   |
                                             | (API, UI backend)  |
                                             +--------------------+
```

- **Kamailio** gives phones one SIP address. It sends each phone's REGISTERs
  to one READY node (hashing the user), spreads calls over READY nodes, and
  probes every node with OPTIONS every 4 seconds. A node that answers anything
  but 200 (draining, unhealthy, still joining) or nothing at all leaves
  rotation within 15 seconds (three failed probes: 3 × 4 s plus a 1.5 s reply
  timeout). Kamailio adds itself to each registration's
  `Path`, so every Hello node reaches every phone through Kamailio, whichever
  node took the registration.
- **SIP nodes** are interchangeable. Registrations, active calls, trunk state
  and cluster membership live in Valkey; configuration is a snapshot loaded from
  PostgreSQL and kept in memory, so a node never asks PostgreSQL anything while
  handling a call.
- **Valkey** runs as a primary and a replica watched by three Sentinels. When
  the primary fails, the Sentinels promote the replica and Hello follows.
- **hello-control** runs as two or more replicas. It is not in the call path.

## Node states

Every node publishes its state in Valkey every second (`HELLO_MEMBER_HEARTBEAT`,
at most a third of the 4 s record TTL). The Cluster page and
`GET /api/v1/cluster` show them.

| State     | Meaning                                                                | Readiness | Takes new calls |
| --------- | ---------------------------------------------------------------------- | --------- | --------------- |
| JOINING   | Starting: configuration snapshot, Valkey or the SIP listener not ready | 503       | No              |
| READY     | Serving                                                                | 200       | Yes             |
| DRAINING  | Finishing its calls before stopping                                    | 503       | No              |
| UNHEALTHY | A required dependency (Valkey; PostgreSQL for hello-control) fails     | 503       | No              |
| OFFLINE   | No heartbeat for 4 seconds; listed for 10 minutes                      | —         | No              |

An OFFLINE node is a failure only if it crashed and nothing replaced it. The
API marks the two harmless cases on the member, which stays OFFLINE (its
unclaimed calls are still taken over) and listed for the 10 minutes:
`left: true` when the node shut down cleanly (it called Leave at exit), and
`superseded: true` when a live node of the same kind and slot started after it
(on kw a node's ID is its pod name, so a Deployment's replacement pod has a new
one; the slot is the ID without that generated suffix, `hello-sip-1` for both).
kw's hello-sip-1 and hello-sip-2 now run under fixed IDs of those names, so a
replacement process reuses the ID (its incarnation tells the processes apart,
see the takeover rules below) and leaves no stale member at all; hello-control
replicas still have pod-name IDs and rely on the two flags.
The dashboard, the sidebar badge, Diagnostics, the node-health AI detector and
`hello_cluster_members` (state `LEFT` instead of `OFFLINE`) do not alert on
them; the Cluster page lists them as Left. A crash keeps alerting until the
tombstone expires or a replacement starts.

## Failure table

This table is normative: it is what Hello does. The Test column names the test that checks each row (`test/integration/failure_test.go` unless noted); rows marked "documented, not automated" describe behaviour no test proves yet.

| Failure                                        | Detected by                                                            | Node reports                                                                                                                                   | Still works                                                                                                                                                                                                                                                                                                                                                                                         | Recovery                                                                                          | Test                                                                                                             |
| ---------------------------------------------- | ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| **SIP node dies**                              | Kamailio OPTIONS probe (≤15 s); membership expires (4 s)               | Other nodes: the dead node goes OFFLINE (`hello_node_state`)                                                                                   | New registrations and calls through the survivor. Phones registered through the dead node stay reachable: their binding is in Valkey and their Path points through Kamailio                                                                                                                                                                                                                         | Restart the node; it joins and Kamailio adds it back after a 200 probe                            | `TestKillSIPNodeDuringRegister`, `TestKillSIPNodeDuringRinging`, `TestKillSIPNodeDuringCall`                     |
| **Call in progress on the dying node**         | Membership expires (4 s), or the node's restart in place               | `hello_dialog_takeovers_total` on the taker; the call re-homes with `ha: taken-over` in its CDR trace                                          | The call continues: both endpoints re-INVITEd to the taker's relay (lab: within 4.6 s of the kill), and hangup, hold and recording work there                                                                                                                                                                                                                                                       | Automatic; a call that cannot be saved is counted in `hello_zombie_calls_total`                   | `TestKillSIPNodeDuringCall` (full takeover), `TestRestartSIPNodeInPlaceDuringCall`, `TestTakeoverScenarioMatrix` |
| **SIP node drained (maintenance)**             | Operator action or SIGTERM                                             | DRAINING (`hello_node_state`); calls left to hand off (`hello_drain_active_calls`)                                                             | Its established calls, handed to a READY node (in-call HA handoff), and its trunk registrations, which move to another node; new INVITEs to it get 503 and Kamailio stops sending it work within 15 s                                                                                                                                                                                               | The node exits once its calls are handed off or ended, or at `HELLO_DRAIN_TIMEOUT`                | `TestDrainKeepsCallsAndExits`, `TestRollingUpgrade`                                                              |
| **Valkey primary dies**                        | Sentinels (5 s down-after) promote the replica                         | UNHEALTHY with 503 + `Retry-After` until Hello reconnects; READY within 15 s of promotion (`hello_node_state`, `hello_valkey_failovers_total`) | Established calls; after promotion, everything                                                                                                                                                                                                                                                                                                                                                      | Automatic; the old primary rejoins as a replica when restarted                                    | `TestValkeyFailover`                                                                                             |
| **Writes lost in a Valkey failover**           | —                                                                      | —                                                                                                                                              | Replication is asynchronous, so registrations written in the last moment before the failure can be lost                                                                                                                                                                                                                                                                                             | Phones restore them on their next refresh                                                         | Documented, not automated                                                                                        |
| **All of Valkey unavailable**                  | Every node's readiness check                                           | UNHEALTHY (`hello_node_state`); new REGISTER/INVITE get 503 + `Retry-After`                                                                    | Established calls                                                                                                                                                                                                                                                                                                                                                                                   | Automatic when Valkey returns                                                                     | `TestPartitionFromValkey` (one node), Phase 1 `TestStateUnavailable`                                             |
| **SIP node cut off from Valkey (partition)**   | Its readiness check                                                    | UNHEALTHY within 15 s (`hello_node_state`); Kamailio stops sending it new work                                                                 | Everything on the other node. Its own established calls are documented, not automated                                                                                                                                                                                                                                                                                                               | READY within 15 s of reconnecting                                                                 | `TestPartitionFromValkey` (readiness and Kamailio removal)                                                       |
| **PostgreSQL unavailable or restarting**       | hello-control readiness; hello-sip snapshot reload errors              | hello-control UNHEALTHY; hello-sip stays READY and counts reload failures (`hello_snapshot_reload_failures_total`)                             | Registration and calling from the last snapshot; CDRs buffer in memory (up to 1000 per node, then counted drops: `hello_cdr_dropped_total`)                                                                                                                                                                                                                                                         | Snapshots resume and buffered CDRs flush when PostgreSQL returns                                  | `TestPostgresOutage`                                                                                             |
| **hello-control (all replicas) down**          | Load balancer / your monitoring                                        | —                                                                                                                                              | All telephony                                                                                                                                                                                                                                                                                                                                                                                       | Restart; management resumes                                                                       | `TestControlPlaneRestart`                                                                                        |
| **Stale configuration**                        | `hello_config_revision_lag` and the Cluster page                       | Revision lag per node                                                                                                                          | Calls route by the node's last valid configuration                                                                                                                                                                                                                                                                                                                                                  | The node reloads on the next NOTIFY or 30 s poll                                                  | Phase 1 `TestSnapshotReloadOnNotify`                                                                             |
| **Invalid configuration revision**             | Compile failure on the node                                            | `hello_routing_config_invalid` = 1                                                                                                             | Routing from the last valid revision; new extensions are still found                                                                                                                                                                                                                                                                                                                                | Fix the configuration through the API                                                             | Phase 2 `TestKeepLastGoodRouter`, `TestFrozenMarkerSurvivesDNSRebuild`                                           |
| **Duplicate registration updates**             | —                                                                      | —                                                                                                                                              | The same contact through two nodes overwrites one binding, never duplicates                                                                                                                                                                                                                                                                                                                         | —                                                                                                 | Phase 1 `TestRegisterBindings`                                                                                   |
| **Delayed or out-of-order NOTIFY**             | —                                                                      | —                                                                                                                                              | Each reload reads the database's current revision, so a late NOTIFY never applies an older one; a reload that finds the revision already loaded is skipped. A missed NOTIFY is caught by the 30 s poll                                                                                                                                                                                              | Automatic                                                                                         | Phase 1 `TestWatcherNotifyPollAndRetention`                                                                      |
| **Kamailio dies** (one of two, behind the VIP) | kube-vip's Lease (5 s) and Kamailio's own `/healthz`, probed every 2 s | —                                                                                                                                              | Registered phones: the survivor takes the VIP and the next request reaches it. Calls in progress: media keeps flowing, and a BYE from either side, a takeover re-INVITE and an INVITE to a phone registered before the loss all go through the survivor, because the addresses a dialog names (Via, Path, Record-Route) are the VIP. Requests in flight at the moment of the loss are retransmitted | Automatic: the restarted instance joins the election and does not take the VIP from a healthy one | `test/kindha` `TestKamailioHA` (CI job `kamailio-ha`): [Kamailio pair](#kamailio-pair)                           |

## Conference rooms

A conference room is mixed on one node, its owner, chosen by a lease in Valkey.
A join that lands on another node is forwarded to the owner, and a room
survives the death of the owner or of a node holding forwarded callers: the
participants are taken over like any call and the room moves to a survivor.
Participants are asked for the PIN again after a recovery, and a leader's mutes
and lock are not carried over. The design, the table of what each failure does
and the limitations are in [Conference rooms](conference.md#high-availability).

## Drain a node for maintenance

1. Open **Cluster** and choose **Drain** on the node, or call
   `POST /api/v1/cluster/nodes/{id}/drain`. Hello warns before draining the
   last READY SIP node.
2. The node turns DRAINING: Kamailio stops sending it new calls and
   registrations within 15 seconds, phones that try anyway are told to retry
   elsewhere (503 + `Retry-After`), and its trunk registrations move to another
   node.
3. Its active calls are handed to a READY SIP node (see
   [Handoff on drain](#handoff-on-drain)): each continues there after a short
   audio gap, and the draining node exits once it holds no call. A call no
   node can take (no READY node, or one still ringing) continues to its end
   on the draining node; after `HELLO_DRAIN_TIMEOUT` (default 2 hours) the
   node hangs up any calls that remain and exits.
4. Stopping a node with SIGTERM (`docker compose stop`, a Kubernetes pod
   termination) drains it the same way; give it a grace period long enough
   for the handoff (seconds) and for whatever calls no node can take.

Cancel a drain with **Undrain** or `DELETE /api/v1/cluster/nodes/{id}/drain`
before the node starts exiting.

## Roll an upgrade

Upgrade one SIP node at a time:

1. Drain it and wait for it to exit.
2. Start the new version; wait until the Cluster page shows it READY.
3. Repeat for the next node.

Active calls are handed to the READY node as the other drains; new calls go
to whichever nodes are READY. `TestRollingUpgrade` does exactly this with a
call active throughout: the call moves to the upgraded node and is never
dropped. Upgrade hello-control replicas one at a time the same way; they
carry no calls.

### When a deployer restarts every node at once

A deployer that applies all SIP nodes together (kw: Kuvryn Sync applies the
hello-sip-1 and hello-sip-2 Deployments in the same second, each `Recreate`)
would stop both: the draining node would have no READY survivor and new calls
no node. Set `HELLO_ROLLOUT_WAIT` (kw: 150s) and SIGTERM takes a rollout gate
before the drain starts:

- The node takes the one-per-kind restart token in Valkey
  (`hello:rollout:sip`, expiring after 2 minutes without renewal; held by the
  node's slot, its ID without a Deployment pod's generated suffix, so a
  replacement pod with a new name releases what its predecessor took) and waits,
  still READY and serving, until another SIP node is READY. Two nodes told to
  stop together get the token one after the other.
- It keeps the token while it drains and after it exits. Its replacement
  releases the token once it is READY, and only then may the peer stop. A
  replacement that never becomes READY holds the peer back for at most the
  token's 2 minutes.
- A node with no SIP peer stops at once. A node that waited the full
  `HELLO_ROLLOUT_WAIT`, or finds Valkey unreachable, drains anyway: a
  shutdown never hangs. The pod's termination grace period must exceed the
  wait plus `HELLO_DRAIN_TIMEOUT` (`test/deploy` checks kw).

Once draining, a node whose calls have no READY survivor yet keeps them and
only waits: the survivors scan DRAINING nodes' handoff-marked calls on every
takeover pass, so the calls move as soon as a peer is READY, and only the
drain timeout ends them. The default is off (`0`), which keeps the lab and
the chart's ordered StatefulSet rollout as they were.

## In-call HA (Level 4)

Every call anchors through the owning node's media relay, and the node
continuously writes each connected call's recovery state to Valkey — both
legs' full dialog data (Call-IDs, tags, CSeq counters, route sets, contacts,
remote targets, both sides' negotiated SDP), the relay ports, the call's
phase (talking, hold, transferring, recording, announcement, voicemail) with
what a taker needs to resume it, the caller and destination numbers and the
CDR correlation. The CSeq a taker continues from is past every request the
owner sent on the dialog, including the NOTIFYs of a transfer. Writes happen
off the SIP transaction path: on every state change and on a 1 s heartbeat
(the membership heartbeat). A record expires 10 s after its last write —
past detection (4 s), the takeover poll and both re-INVITEs — and a record
written within the last 2 s (two heartbeats) is never claimed on OFFLINE
grounds: its owner is alive. Calls that come out of a
transfer (the blind transfer's new call, the attended transfer's bridge)
keep the original call's relay and replicate like any call.

When membership marks a node OFFLINE (4 s without a heartbeat), each
surviving node scans for that node's unclaimed dialogs on a jittered
1–1.5 s poll (`HELLO_HA_TAKEOVER_POLL` 1 s, `HELLO_HA_TAKEOVER_JITTER`
500 ms) and claims them
atomically (a Valkey Lua script: no claim wins twice). The taker rebuilds
both legs from the replicated state — the endpoints see the same Call-IDs
and tags, and the CSeq continues the old owner's counter — allocates fresh
relay ports, and re-INVITEs both endpoints. Kamailio's failure route sends
in-dialog requests that reach a dead node (408/503, or a 1.5 s timeout) to
another hello-sip node, which answers from the replicated state. The claim
is released once both legs are re-homed and the record names the taker (so
no survivor takes the call twice); a claim is a compare-and-set on the
record as the claimant read it, so a record rewritten in between (the
owner alive after all, or a taker that already finished) is never claimed.
A slow owner that reappears mid-takeover finds the taker's claim or record
on its next heartbeat and yields. Either re-INVITE
failing (an endpoint that died too, or rejects with 488/603) closes the call
one-sidedly — the surviving leg gets a normal BYE and CDR — and the zombie
is counted.

Calls Hello answered itself — a caller in voicemail, or an announcement
destination — have one dialog, the caller's. The taker re-INVITEs the
caller onto media of its own (a fresh voicemail anchor, or a relay) and
restarts the application from its beginning: voicemail replays the greeting
and beep and records afresh (what the caller had said on the dead node is
lost), an announcement plays from the top and then hangs up as before. A
caller in an IVR menu is recovered the same way (the relay carries the
caller): the menu restarts from its greeting on the taker. Once the menu has
connected the caller to a phone the call is an ordinary two-leg call and is
taken over as one. The taker can dial out from a restarted menu too. The caller's number and
media offer come from the replicated dialog and the routing context from the
snapshot, so it rings the option's extension, ring group or external number
with new legs offered the taker's own relay, and the answering leg becomes the
callee dialog of the taken-over call (the caller's BYE reaches it). The
caller's display name and device name are not replicated; the caller's own
phone is skipped by its replicated contact instead. What cannot be recovered
is not treated as a no-answer: a paging destination (a page's member dialogs
are not replicated, so a survivor could not hang them up) and a dial-out whose
replicated state lacks the caller's offer send the call to the menu's
invalid-choice exit instead (`TestIVRTakeoverDialsOut`,
`TestIVRTakeoverUnrecoverableFallsBack`).

Each claim is counted against the dead node in Valkey. Past the dialog TTL,
exactly one survivor counts the dead node's calls that nobody claimed (its
live calls at death less the claims) in `hello_zombie_calls_total`: a call
that was taken over is never counted, a takeover that failed is counted
once by its taker.

### Restart in place

Kubernetes restarts a crashed container in place, under the same pod name,
within seconds — possibly inside membership's 4 s OFFLINE window, so the node never
goes OFFLINE and the takeover above would never start, while its new
process knows nothing of the old one's calls. Every hello-sip process
therefore has a random **incarnation** id: membership publishes it
(`incarnation` on `GET /api/v1/cluster` members) and every replicated
dialog record carries its owner's (`ownerIncarnation`). A record whose
owner node is alive under another incarnation belongs to a dead process:

- Every READY node — the restarted one included, which is the fastest —
  scans the dialogs of a node for 15 s (the dialog TTL plus margin) after
  first seeing its incarnation or seeing it change, and claims those of a
  dead incarnation at once, without the freshness wait an OFFLINE takeover
  has. The restarted node reclaims its own calls this way.
- An in-dialog request (BYE, re-INVITE, UPDATE, INFO, REFER) that reaches a
  node holding no such dialog is checked against the replicated records of
  both legs before it could be answered 481. When the record's owner
  process is gone (a dead incarnation, OFFLINE, or a handoff), the node
  claims the call on the spot: a BYE is answered 200, the other endpoint
  gets its BYE and the call its CDR, with no re-INVITE; a re-INVITE or
  UPDATE starts the full takeover and is answered 491 (the endpoint retries
  after the takeover's re-INVITE); anything else starts it and gets 503.
  While the owner is alive or another node's takeover is in flight the
  request gets 503 with `Retry-After`, never 481. The request must carry
  the dialog's tags from the endpoint's source or a trusted proxy.

Claims on a dead incarnation are counted apart from the node's OFFLINE
takeovers, so they never offset the zombie count of a later death of the
same node. Proven by `TestTakeoverRestartInPlace`,
`TestTakeoverOnDemandBye` and `TestTakeoverIncarnationWatch`
(`internal/sip`), `TestDialogClaimStaleIncarnation` (`internal/livestate`,
real Valkey) and the lab's `TestRestartSIPNodeInPlaceDuringCall`, which
kills a hello-sip container and starts it again at once under a live call.

### What the CDR says

A taken-over call is one logical call with one CDR, written by the node
that ends it. The replicated record carries what that CDR needs from the
call's first node: its start, ring and answer times, direction, route,
trunk and routing trace. So the CDR starts when the call started (not at
the takeover), its billable time runs from the original answer, and its
trace holds the whole call. A node that yields a call (after a handoff, or
a slow owner that finds its call taken) writes no CDR and leaves the live
call record alone: both are the taker's, under the same correlation id.
`terminationSide` is the side that sent the BYE.

The trace's takeover step reads `ha: taken over from <node> in <t>
(media gap <g>)`, followed by where the gap was measured from:

- `<t>` is the takeover itself, from the claim to both endpoints answering
  the re-INVITEs (the ≤3 s target).
- `<g>` is the media gap, from the dead owner's last sign of life — its
  last replication write, at most one heartbeat (1 s) before it died — to
  both endpoints re-homed. It includes the time until the death was
  noticed, so it is an upper bound of the audio the call lost, at most 1 s
  over it (and subject to clock skew between the two nodes). After a
  handoff the draining node relays until the claim, so the gap runs from
  the claim and equals `<t>`. A call hung up before any re-INVITE (the BYE
  case above) has no takeover step: it never had media restored.

### Handoff on drain

A draining node (an operator drain or SIGTERM) does not keep its calls to
the end: it marks each recoverable call's record for handoff, and a READY
survivor claims such a dialog of a DRAINING node at once — without waiting
for the node to go OFFLINE — and takes it over exactly as after a crash
(re-INVITEs, a gap of the re-INVITE round trip, CDR and live view `taken-over`). The draining node
yields its copy as soon as the survivor holds the call: it closes its media
without a BYE to the endpoints and without a CDR or touching the live call
record (both are the survivor's), stops refreshing the live record once the
call is claimed, and answers any in-dialog request
that still reaches it for the call with 503, which Kamailio's in-dialog
failure route retries on a survivor. With no READY survivor nothing is
claimed and the drain timeout applies as before; a cancelled drain takes
back the calls nobody claimed yet. Proven by `TestHandoffOnDrain`, `TestHandoffKeepsLiveCallAndCDR` and
`TestHandoffCancelledDrain` (`internal/sip`) and the lab's
`TestDrainKeepsCallsAndExits` and `TestRollingUpgrade`.

### Scenario matrix

Each row is proven by a test (`TestKillSIPNodeDuringCall` in
`test/integration/failure_test.go`, `TestTakeoverScenarioMatrix` and friends
in `internal/sip`):

| The node dies during                   | What the users see                                                                                                                    |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| A connected call                       | An audio gap per the table at the top (about 4 s after a crash), call continues, hang up normally                                     |
| A held call                            | The hold direction survives the takeover, and the held party's music on hold restarts on the taker                                    |
| A blind transfer, target still ringing | The original call continues untransferred; the transferor gets a final NOTIFY (503); Kamailio's INVITE timer stops the target ringing |
| A blind transfer, target answered      | The transferred call (caller and target) continues                                                                                    |
| An attended transfer's consultation    | Both calls (the held original and the consultation) continue                                                                          |
| An attended transfer, bridged          | The bridged call continues                                                                                                            |
| A recording call                       | The recording continues on the taker (second MinIO object, same correlation)                                                          |
| An announcement                        | The announcement restarts from its beginning                                                                                          |
| An announcement destination            | The announcement restarts from its beginning, then the call ends as before                                                            |
| A voicemail recording or prompt        | The caller stays connected; the greeting restarts and the message is recorded on the taker                                            |
| Ringing                                | Phase 3 behaviour: the caller gets a final response, nothing hangs                                                                    |

`TestTakeoverScenarioMatrix` runs every row in-process; the lab suite kills
real nodes under a connected call (`TestKillSIPNodeDuringCall`,
`TestTakeoverMediaGap`, `TestHonestyFlags`), a voicemail call
(`TestKillSIPNodeDuringVoicemail`) and a ringing call. `TestDoubleFailure`
kills a second node while a survivor is mid-takeover of a different call
(in-process: the lab runs two SIP nodes).

A taker reaches each endpoint through the dialog's route set, first hop
first: the edge's Hello-facing socket. Kamailio double-record-routes, and
its phone-facing entry (on kw, a NodePort address inside a trusted range)
is never a hop Hello may use. `TestTakeoverEdgeRouteSet` keeps the two
apart.

Not recovered after a takeover: a REFER (transfer) on a taken-over call is
answered 481, and a SIP INFO DTMF digit is not matched (in-band RFC 2833
DTMF, including `*1` recording, works). A transfer still in progress when
the node dies is abandoned, as in the table.

### The two named limitations

- **Kamailio is the path of in-dialog signalling.** A call's dialogs route
  through the Kamailio that record-routed them. With two Kamailio behind a
  VIP ([Kamailio pair](#kamailio-pair)) the address those dialogs name
  survives either instance; losing both breaks in-dialog signalling of the
  calls they routed, takeover included, until one returns. A single Kamailio
  (the lab, a small install) is that loss at the first failure.
- **Losing Valkey and the node together loses the call.** The replicated
  state lives in Valkey; when both are gone no survivor can rebuild the
  dialogs. Such calls are counted in `hello_zombie_calls_total`, never
  silent. A call whose replication was failing when its node died (see
  `hello_dialog_replicated_total{result="failed"}`) is unrecoverable the
  same way.

## Production guidance

- **Kamailio:** run two, with identical configuration; they are stateless
  apart from their dispatcher view, so either serves any phone.
  - Put them behind a virtual IP that follows the healthy one
    ([Kamailio pair](#kamailio-pair)) and give phones the VIP as
    `KAMAILIO_PUBLIC_HOST`. Phones keep one address, and established dialogs
    survive an instance's loss because `Path`, `Record-Route` and `Via` name
    the VIP, not the instance. kw and the Helm chart (`kamailio.vip`) use
    kube-vip in ARP mode; keepalived (VRRP) does the same job if you run it
    already, with the sockets bound to the VIP as in the cfg.
  - DNS SRV also works for phones that support it, but when one Kamailio dies
    its phones are unreachable until their next REGISTER, and its dialogs lose
    in-dialog signalling (re-INVITE, BYE).
  - Keep phone registration intervals short (300–600 s) so phones recover
    quickly from any edge failure.
  - Set `KAMAILIO_PIKE_DENSITY` (requests per source IP per 2 s, default 200)
    to cover your biggest NAT: the site with the most phones behind one public
    IP.
  - Keep `KAMAILIO_INVITE_TIMEOUT` (ms) above `HELLO_SIP_RING_TIMEOUT`, so
    Hello ends unanswered calls and Kamailio answers 408 only when a node dies
    mid-ring.
  - Monitor `/metrics` on port 9090 (internal): `kamailio_dispatcher_set_active`
    (READY nodes in rotation), `kamailio_dispatcher_destination_active` (per
    node, 1 or 0), `kamailio_dispatcher_transitions_total` (per node and
    up/down), `kamailio_pike_blocked_total` (requests refused by flood
    protection).
  - See the dispatcher view with
    `docker compose -p <project> -f deploy/docker-compose/compose.yaml exec kamailio kamcmd dispatcher.list`.
    Flags: `AP` active, `TP` trying, `IP` inactive.
- **Kubernetes:** give each hello-sip node a headless Service
  (`clusterIP: None`) and name those in Kamailio's dispatcher list, so the
  names resolve straight to the pods. Behind a ClusterIP, a CNI with
  socket-level load balancing (Cilium socket-LB) force-terminates
  Kamailio's UDP socket when the backend pod goes, and Kamailio exits. Hello
  answers every probe and request at the address it came from, and
  Kamailio asks for that (rport) on what it sends Hello, so a replaced
  Kamailio pod is probed UP without flushing conntrack. Kamailio
  re-resolves those names every 5s (`ds_dns_mode` 1|4, `ds_dns_interval`
  5; answers cached for the record's TTL), keeping each node's probing state, so a
  hostNetwork pod that returns on another node is accepted as an in-dialog
  target again within seconds, with no `dispatcher.reload`.
- **Trusted proxies:** set `HELLO_SIP_TRUSTED_PROXIES` to the Hello-facing
  address of every Kamailio, and nothing else. Hello trusts the client address
  and `Path` that Kamailio sends, and ignores them from anywhere else; a
  Kamailio missing from the list breaks registration through it. A
  taken-over call accepts its endpoints' in-dialog requests by dialog
  identity (Call-ID and tags) from any trusted proxy address, since the
  edge reaches the taker from another address than the one the dead owner
  recorded.
- **Valkey:** one primary and at least one replica. Hello uses
  `HELLO_VALKEY_SENTINELS` and `HELLO_VALKEY_MASTER`. Hello needs Valkey 9 or
  later.
  - Run an odd number of Sentinels, at least three, with quorum 2, on separate
    hosts or zones, so a majority survives one failure.
  - Set `down-after-milliseconds` to about 5000. The lab uses 5000, and
    promotion completes in about 5 seconds.
  - Enable `resolve-hostnames` and `announce-hostnames` when addresses change
    (containers, Kubernetes), and name Valkey and Sentinel by hostname.
  - A restarted old primary rejoins as a replica.
  - Replication is asynchronous: a failover can lose the last writes before it
    (a registration or call state written just before the primary died).
- **PostgreSQL:** Hello tolerates PostgreSQL outages for telephony; for
  management availability run PostgreSQL with automatic failover (an operator
  such as CloudNativePG, or Patroni). [docs/kubernetes.md](kubernetes.md) covers this for Kubernetes.

## Kamailio pair

Two Kamailio run on two nodes of one layer-2 network (kw: master-11 and
master-12), identically configured, with one virtual IP that follows a
healthy instance. Either serves any phone and any in-dialog request that
carries a `Route`: Kamailio keeps nothing Hello needs (registrations are in
Valkey, the dispatcher view is rebuilt by probes within seconds).

**How it fits together.**

- **The VIP** (`KAMAILIO_EDGE_VIP`, kw `192.168.10.109`) is held by one
  instance at a time. Each pod runs a kube-vip container beside Kamailio: both
  elect one holder with a Lease (`kamailio-vip`, 5 s), the holder adds the VIP
  to the node's interface and announces it (ARP), and a loser, a crash, a
  node loss or a rollout moves it. The container's liveness probe asks the
  Kamailio next to it, so a Kamailio that stops answering makes the holder
  release the VIP within about 6 s.
- **Every address a dialog names is the VIP.** The phones sockets and the
  Hello-facing socket (5070) bind it (`ip_free_bind`, so the instance that
  does not hold it yet can bind it), `KAMAILIO_PUBLIC_HOST` is it, so Via,
  `Path` and both `Record-Route`s survive an instance. Hello's route set for a
  call (a hangup, a hold, a takeover re-INVITE, the first hop of which is the
  Hello-facing `Record-Route`) reaches whichever instance holds it.
- **Dispatcher probes** leave from a socket on each node's own address (UDP
  5071), not from the VIP: Hello answers the address that probed, and a probe
  from the VIP would be answered to whichever node holds it.
- **One at a time.** The Kamailio Deployment is one Deployment with two
  replicas, `RollingUpdate` with `maxSurge: 0` and `maxUnavailable: 1` (a
  hostNetwork surge pod cannot bind the node's ports), required hostname
  anti-affinity, `minReadySeconds`, and a PodDisruptionBudget allowing one
  disruption. A config or image change, a node drain or a reboot touches one
  instance; `test/deploy` guards the shape.
- **The node address.** While phones are provisioned with an instance's own
  address (kw: `192.168.10.101`), `KAMAILIO_LEGACY_IP` keeps serving it on
  separate sockets with the node address in Via, Path and Record-Route, exactly
  as before the VIP. The two families never mix: a request is answered, and
  its dialog routed, from the family of the socket it arrived on. See
  [Moving phones to a new Kamailio address](provisioning.md#moving-phones-to-a-new-kamailio-address).

**What a failure does.** Phones registered through the lost instance stay
registered (their bindings are in Valkey). The VIP answers from the survivor
within the Lease (5 s) plus Kamailio's own detection; a request sent in the
gap is retransmitted by the sender and delivered when the VIP is back. A
transaction in flight inside the lost instance is lost (a retransmission or a
re-REGISTER covers it). The restarted instance does not take the VIP back
from a healthy one. If both instances are down, nothing new reaches the
cluster, as with one.

**Split brain.** If the nodes cannot see each other's Lease (a partition
between them and the API server), both may hold the VIP and phones reach
whichever ARP wins. That degrades rather than breaks: both serve any phone and
Kamailio holds no state. Alert from outside the cluster on the VIP not
answering a SIP `OPTIONS` (any holder answers 200), which catches a VIP that
nobody holds as well.

**Proof.** `test/kindha` (CI job `kamailio-ha`) applies the kw edge manifests
to a kind cluster and plays the phones and the Hello nodes. With calls up it
removes the active instance, once by deleting its pod (kube-vip hands the VIP
over) and once by freezing its node (a crash: nothing is released), and
requires: the VIP answers from the survivor; a BYE from either side ends its
call within 5 s of that; a takeover re-INVITE sent during the outage is
completed after it through the route set the call already had; a phone
registered before the loss receives an INVITE after it with no re-REGISTER;
the returning instance does not take the VIP back. It then restarts both
instances one at a time and requires one Kamailio Ready at every sample and
the VIP never dark for longer than a failover.

**Live check on a real cluster (do it once, on kw).** Register two phones, start
a call, delete the pod of the node that holds the VIP
(`ip -4 addr show dev <interface> | grep <VIP>` on each node tells which), and
hang up from either phone. Then power off that node and check the same. The
Cluster page shows both SIP nodes READY throughout and no registration is lost.
