# High availability

Hello keeps taking registrations and calls when any single SIP node, the Valkey
primary, PostgreSQL or the control plane fails. This page explains how, says
exactly what each failure does, and shows how to drain nodes and roll upgrades.
Every row of the failure table is proven by an automated test in
`test/integration/failure_test.go`, except where a row says otherwise.

## What Hello guarantees

| Level | Guarantee                                                                  | Status     |
| ----- | -------------------------------------------------------------------------- | ---------- |
| 1     | Losing hello-control or the UI does not interrupt telephony                | Guaranteed |
| 2     | When a SIP node fails, phones register through another node                | Guaranteed |
| 3     | When a SIP node fails, new internal, inbound and outbound calls still work | Guaranteed |
| 4     | An established call survives loss of the node controlling it               | Guaranteed |

Level 4 (Phase 7, in-call HA): **a live call survives the death of its SIP
node with ≤3 s audio gap, when the cluster retains Valkey and at least one
Kamailio.** The call is taken over by a surviving node from its replicated
dialog state, both endpoints are re-INVITEd to the taker's media relay, and
the call can be held, recorded and hung up as before (a transfer requested
after the takeover is refused; see below). The CDR trace and the live view
(`GET /api/v1/calls` field `ha`, a "Taken over" badge on the Active Calls
page) mark such a call `taken-over`; a call no survivor could save (see the
two limitations below) is counted in `hello_zombie_calls_total` and never
silently dropped.

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

Every node publishes its state in Valkey every 5 seconds. The Cluster page and
`GET /api/v1/cluster` show them.

| State     | Meaning                                                                | Readiness | Takes new calls |
| --------- | ---------------------------------------------------------------------- | --------- | --------------- |
| JOINING   | Starting: configuration snapshot, Valkey or the SIP listener not ready | 503       | No              |
| READY     | Serving                                                                | 200       | Yes             |
| DRAINING  | Finishing its calls before stopping                                    | 503       | No              |
| UNHEALTHY | A required dependency (Valkey; PostgreSQL for hello-control) fails     | 503       | No              |
| OFFLINE   | No heartbeat for 15 seconds; listed for 10 minutes                     | —         | No              |

## Failure table

This table is normative: it is what Hello does. The Test column names the test that checks each row (`test/integration/failure_test.go` unless noted); rows marked "documented, not automated" describe behaviour no test proves yet.

| Failure                                      | Detected by                                               | Node reports                                                                                                                                   | Still works                                                                                                                                                                                            | Recovery                                                                                                                    | Test                                                                                         |
| -------------------------------------------- | --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| **SIP node dies**                            | Kamailio OPTIONS probe (≤15 s); membership expires (15 s) | Other nodes: the dead node goes OFFLINE (`hello_node_state`)                                                                                   | New registrations and calls through the survivor. Phones registered through the dead node stay reachable: their binding is in Valkey and their Path points through Kamailio                            | Restart the node; it joins and Kamailio adds it back after a 200 probe                                                      | `TestKillSIPNodeDuringRegister`, `TestKillSIPNodeDuringRinging`, `TestKillSIPNodeDuringCall` |
| **Call in progress on the dying node**       | Membership expires (15 s)                                 | `hello_dialog_takeovers_total` on the taker; the call re-homes with `ha: taken-over` in its CDR trace                                          | The call continues: both endpoints re-INVITEd to the taker's relay within 3 s of the claim, and hangup, hold, transfer and recording work there                                                        | Automatic; a call that cannot be saved is counted in `hello_zombie_calls_total`                                             | `TestKillSIPNodeDuringCall` (full takeover), `TestTakeoverScenarioMatrix`                    |
| **SIP node drained (maintenance)**           | Operator action or SIGTERM                                | DRAINING (`hello_node_state`); calls left to finish (`hello_drain_active_calls`)                                                               | Its established calls and its trunk registrations, which move to another node; new INVITEs to it get 503 and Kamailio stops sending it work within 15 s                                                | The node exits after its last call or `HELLO_DRAIN_TIMEOUT`                                                                 | `TestDrainKeepsCallsAndExits`, `TestRollingUpgrade`                                          |
| **Valkey primary dies**                      | Sentinels (5 s down-after) promote the replica            | UNHEALTHY with 503 + `Retry-After` until Hello reconnects; READY within 15 s of promotion (`hello_node_state`, `hello_valkey_failovers_total`) | Established calls; after promotion, everything                                                                                                                                                         | Automatic; the old primary rejoins as a replica when restarted                                                              | `TestValkeyFailover`                                                                         |
| **Writes lost in a Valkey failover**         | —                                                         | —                                                                                                                                              | Replication is asynchronous, so registrations written in the last moment before the failure can be lost                                                                                                | Phones restore them on their next refresh                                                                                   | Documented, not automated                                                                    |
| **All of Valkey unavailable**                | Every node's readiness check                              | UNHEALTHY (`hello_node_state`); new REGISTER/INVITE get 503 + `Retry-After`                                                                    | Established calls                                                                                                                                                                                      | Automatic when Valkey returns                                                                                               | `TestPartitionFromValkey` (one node), Phase 1 `TestStateUnavailable`                         |
| **SIP node cut off from Valkey (partition)** | Its readiness check                                       | UNHEALTHY within 15 s (`hello_node_state`); Kamailio stops sending it new work                                                                 | Everything on the other node. Its own established calls are documented, not automated                                                                                                                  | READY within 15 s of reconnecting                                                                                           | `TestPartitionFromValkey` (readiness and Kamailio removal)                                   |
| **PostgreSQL unavailable or restarting**     | hello-control readiness; hello-sip snapshot reload errors | hello-control UNHEALTHY; hello-sip stays READY and counts reload failures (`hello_snapshot_reload_failures_total`)                             | Registration and calling from the last snapshot; CDRs buffer in memory (up to 1000 per node, then counted drops: `hello_cdr_dropped_total`)                                                            | Snapshots resume and buffered CDRs flush when PostgreSQL returns                                                            | `TestPostgresOutage`                                                                         |
| **hello-control (all replicas) down**        | Load balancer / your monitoring                           | —                                                                                                                                              | All telephony                                                                                                                                                                                          | Restart; management resumes                                                                                                 | `TestControlPlaneRestart`                                                                    |
| **Stale configuration**                      | `hello_config_revision_lag` and the Cluster page          | Revision lag per node                                                                                                                          | Calls route by the node's last valid configuration                                                                                                                                                     | The node reloads on the next NOTIFY or 30 s poll                                                                            | Phase 1 `TestSnapshotReloadOnNotify`                                                         |
| **Invalid configuration revision**           | Compile failure on the node                               | `hello_routing_config_invalid` = 1                                                                                                             | Routing from the last valid revision; new extensions are still found                                                                                                                                   | Fix the configuration through the API                                                                                       | Phase 2 `TestKeepLastGoodRouter`, `TestFrozenMarkerSurvivesDNSRebuild`                       |
| **Duplicate registration updates**           | —                                                         | —                                                                                                                                              | The same contact through two nodes overwrites one binding, never duplicates                                                                                                                            | —                                                                                                                           | Phase 1 `TestRegisterBindings`                                                               |
| **Delayed or out-of-order NOTIFY**           | —                                                         | —                                                                                                                                              | Each reload reads the database's current revision, so a late NOTIFY never applies an older one; a reload that finds the revision already loaded is skipped. A missed NOTIFY is caught by the 30 s poll | Automatic                                                                                                                   | Phase 1 `TestWatcherNotifyPollAndRetention`                                                  |
| **Kamailio dies**                            | Your monitoring                                           | —                                                                                                                                              | Nothing new reaches the cluster through it                                                                                                                                                             | In production, a second Kamailio takes over the VIP or SRV target (below). In the lab Kamailio is a single point of failure | Manual: [Kamailio loss](#kamailio-loss)                                                      |

## Drain a node for maintenance

1. Open **Cluster** and choose **Drain** on the node, or call
   `POST /api/v1/cluster/nodes/{id}/drain`. Hello warns before draining the
   last READY SIP node.
2. The node turns DRAINING: Kamailio stops sending it new calls and
   registrations within 15 seconds, phones that try anyway are told to retry
   elsewhere (503 + `Retry-After`), and its trunk registrations move to another
   node.
3. Its active calls continue to the end. When the last one ends, the node
   exits. After `HELLO_DRAIN_TIMEOUT` (default 2 hours) it hangs up any calls
   that remain and exits.
4. Stopping a node with SIGTERM (`docker compose stop`, a Kubernetes pod
   termination) drains it the same way; give it a grace period at least as long
   as you are willing to wait for calls.

Cancel a drain with **Undrain** or `DELETE /api/v1/cluster/nodes/{id}/drain`
before the node starts exiting.

## Roll an upgrade

Upgrade one SIP node at a time:

1. Drain it and wait for it to exit.
2. Start the new version; wait until the Cluster page shows it READY.
3. Repeat for the next node.

Active calls finish on the node that set them up; new calls go to whichever
nodes are READY. `TestRollingUpgrade` does exactly this with a call active
throughout. Upgrade hello-control replicas one at a time the same way; they
carry no calls.

## In-call HA (Level 4)

Every call anchors through the owning node's media relay, and the node
continuously writes each connected call's recovery state to Valkey — both
legs' full dialog data (Call-IDs, tags, CSeq counters, route sets, contacts,
remote targets, both sides' negotiated SDP), the relay ports, the call's
phase (talking, hold, transferring, recording, announcement, voicemail) with
what a taker needs to resume it, the caller and destination numbers and the
CDR correlation. The CSeq a taker continues from is past every request the
owner sent on the dialog, including the NOTIFYs of a transfer. Writes happen
off the SIP transaction path: on every state change and on a 5 s heartbeat.
A record expires 30 s after its last heartbeat. Calls that come out of a
transfer (the blind transfer's new call, the attended transfer's bridge)
keep the original call's relay and replicate like any call.

When membership marks a node OFFLINE (15 s), each surviving node scans for
that node's unclaimed dialogs on a jittered 1–3 s poll and claims them
atomically (a Valkey Lua script: no claim wins twice). The taker rebuilds
both legs from the replicated state — the endpoints see the same Call-IDs
and tags, and the CSeq continues the old owner's counter — allocates fresh
relay ports, and re-INVITEs both endpoints. Kamailio's failure route sends
in-dialog requests that reach a dead node (408/503, or a 1.5 s timeout) to
another hello-sip node, which answers from the replicated state. The claim
is released once both legs are re-homed and the record names the taker (so
no survivor takes the call twice); the restarted owner cannot retake
its old calls (its heartbeat finds the taker's record and yields). A slow
owner that reappears mid-takeover yields the same way. Either re-INVITE
failing (an endpoint that died too, or rejects with 488/603) closes the call
one-sidedly — the surviving leg gets a normal BYE and CDR — and the zombie
is counted.

Calls Hello answered itself — a caller in voicemail, or an announcement
destination — have one dialog, the caller's. The taker re-INVITEs the
caller onto media of its own (a fresh voicemail anchor, or a relay) and
restarts the application from its beginning: voicemail replays the greeting
and beep and records afresh (what the caller had said on the dead node is
lost), an announcement plays from the top and then hangs up as before.

Each claim is counted against the dead node in Valkey. Past the dialog TTL,
exactly one survivor counts the dead node's calls that nobody claimed (its
live calls at death less the claims) in `hello_zombie_calls_total`: a call
that was taken over is never counted, a takeover that failed is counted
once by its taker.

### Scenario matrix

Each row is proven by a test (`TestKillSIPNodeDuringCall` in
`test/integration/failure_test.go`, `TestTakeoverScenarioMatrix` and friends
in `internal/sip`):

| The node dies during                   | What the users see                                                                                                                    |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| A connected call                       | ≤3 s audio gap, call continues, hang up normally                                                                                      |
| A held call                            | The hold direction survives the takeover                                                                                              |
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

Not recovered after a takeover: a REFER (transfer) on a taken-over call is
answered 481, and a SIP INFO DTMF digit is not matched (in-band RFC 2833
DTMF, including `*1` recording, works). A transfer still in progress when
the node dies is abandoned, as in the table.

### The two named limitations

- **Kamailio is a single point of in-dialog signalling.** A call's dialogs
  route through the Kamailio that record-routed them. Run at least two with
  a shared VIP (see below); losing all Kamailio breaks in-dialog signalling
  of the calls it routed, takeover included.
- **Losing Valkey and the node together loses the call.** The replicated
  state lives in Valkey; when both are gone no survivor can rebuild the
  dialogs. Such calls are counted in `hello_zombie_calls_total`, never
  silent. A call whose replication was failing when its node died (see
  `hello_dialog_replicated_total{result="failed"}`) is unrecoverable the
  same way.

## Production guidance

- **Kamailio:** run at least two, with identical configuration; they are
  stateless apart from their dispatcher view, so either serves any phone.
  - Prefer a virtual IP shared with keepalived (VRRP) and set
    `KAMAILIO_PUBLIC_HOST` to the VIP. Phones keep one address, and
    established dialogs survive a VIP move because `Record-Route` names the
    VIP, not the instance.
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
- **Trusted proxies:** set `HELLO_SIP_TRUSTED_PROXIES` to the Hello-facing
  address of every Kamailio, and nothing else. Hello trusts the client address
  and `Path` that Kamailio sends, and ignores them from anywhere else; a
  Kamailio missing from the list breaks registration through it.
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
  such as CloudNativePG, or Patroni). Phase 6 covers this in the Kubernetes
  guidance.

## Kamailio loss

Not automated in the lab, which runs one Kamailio. To check a production pair:

1. Register two phones and start a call between them.
2. Stop the active Kamailio.
3. Within your VRRP failover time the standby holds the virtual IP; the phones'
   next requests and new calls go through it.
4. Check the Cluster page: both SIP nodes stay READY; no registrations are lost.
