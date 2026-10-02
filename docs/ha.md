# High availability

Hello keeps taking registrations and calls when any single SIP node, the Valkey
primary, PostgreSQL or the control plane fails. This page explains how, says
exactly what each failure does, and shows how to drain nodes and roll upgrades.
Every row of the failure table is proven by an automated test in
`test/integration/failure_test.go`, except where a row says otherwise.

## What Hello guarantees

| Level | Guarantee                                                                  | Status       |
| ----- | -------------------------------------------------------------------------- | ------------ |
| 1     | Losing hello-control or the UI does not interrupt telephony                | Guaranteed   |
| 2     | When a SIP node fails, phones register through another node                | Guaranteed   |
| 3     | When a SIP node fails, new internal, inbound and outbound calls still work | Guaranteed   |
| 4     | An established call survives loss of the node controlling it               | Not provided |

Level 4 is Phase 7. Today a connected call whose node dies keeps its audio
(media flows directly between the endpoints) but loses its signalling: the
call cannot be held, transferred or cleanly hung up through Hello, and it
leaves the live view within 30 seconds.

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
  probes every node with OPTIONS every 5 seconds. A node that answers anything
  but 200 (draining, unhealthy, still joining) or nothing at all leaves
  rotation within 15 seconds. Kamailio adds itself to each registration's
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

This table is normative: it is what Hello does, and `test/integration/failure_test.go` checks it.

| Failure                                      | Detected by                                               | Node reports                                                                              | Still works                                                                                                                                                                 | Recovery                                                                                                                    | Test                                                                                         |
| -------------------------------------------- | --------------------------------------------------------- | ----------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| **SIP node dies**                            | Kamailio OPTIONS probe (≤15 s); membership expires (15 s) | Other nodes: the dead node goes OFFLINE                                                   | New registrations and calls through the survivor. Phones registered through the dead node stay reachable: their binding is in Valkey and their Path points through Kamailio | Restart the node; it joins and Kamailio adds it back after a 200 probe                                                      | `TestKillSIPNodeDuringRegister`, `TestKillSIPNodeDuringRinging`, `TestKillSIPNodeDuringCall` |
| **Call in progress on the dying node**       | —                                                         | The call leaves the live view within 30 s                                                 | Media continues directly between the phones                                                                                                                                 | Users hang up on their phones (Level 4 is Phase 7)                                                                          | `TestKillSIPNodeDuringCall`                                                                  |
| **Valkey primary dies**                      | Sentinels (3 s down-after) promote the replica            | UNHEALTHY with 503 + `Retry-After` until Hello reconnects; READY within 15 s of promotion | Established calls; after promotion, everything                                                                                                                              | Automatic; the old primary rejoins as a replica when restarted                                                              | `TestValkeyFailover`                                                                         |
| **Writes lost in a Valkey failover**         | —                                                         | —                                                                                         | Replication is asynchronous, so registrations written in the last moment before the failure can be lost                                                                     | Phones restore them on their next refresh                                                                                   | `TestValkeyFailover`                                                                         |
| **All of Valkey unavailable**                | Every node's readiness check                              | UNHEALTHY; new REGISTER/INVITE get 503 + `Retry-After`                                    | Established calls                                                                                                                                                           | Automatic when Valkey returns                                                                                               | `TestPartitionFromValkey` (one node), Phase 1 `TestStateUnavailable`                         |
| **SIP node cut off from Valkey (partition)** | Its readiness check                                       | UNHEALTHY within 15 s; Kamailio stops sending it new work                                 | Its established calls; everything on the other node                                                                                                                         | READY within 15 s of reconnecting                                                                                           | `TestPartitionFromValkey`                                                                    |
| **PostgreSQL unavailable or restarting**     | hello-control readiness; hello-sip snapshot reload errors | hello-control UNHEALTHY; hello-sip stays READY and counts reload failures                 | Registration and calling from the last snapshot; CDRs buffer in memory (up to 1000 per node, then counted drops)                                                            | Snapshots resume and buffered CDRs flush when PostgreSQL returns                                                            | `TestPostgresOutage`                                                                         |
| **hello-control (all replicas) down**        | Load balancer / your monitoring                           | —                                                                                         | All telephony                                                                                                                                                               | Restart; management resumes                                                                                                 | `TestControlPlaneRestart`                                                                    |
| **Stale configuration**                      | `hello_config_revision_lag` and the Cluster page          | Revision lag per node                                                                     | Calls route by the node's last valid configuration                                                                                                                          | The node reloads on the next NOTIFY or 30 s poll                                                                            | Phase 1 `TestSnapshotReloadOnNotify`                                                         |
| **Invalid configuration revision**           | Compile failure on the node                               | `hello_routing_config_invalid` = 1                                                        | Routing from the last valid revision; new extensions are still found                                                                                                        | Fix the configuration through the API                                                                                       | Phase 2 `TestKeepLastGoodRouter`, `TestFrozenMarkerSurvivesDNSRebuild`                       |
| **Duplicate registration updates**           | —                                                         | —                                                                                         | The same contact through two nodes overwrites one binding, never duplicates                                                                                                 | —                                                                                                                           | Phase 1 `TestRegisterBindings`                                                               |
| **Delayed or out-of-order NOTIFY**           | Revision comparison                                       | —                                                                                         | Nodes apply only newer revisions; a missed NOTIFY is caught by the 30 s poll                                                                                                | Automatic                                                                                                                   | Phase 1 snapshot tests                                                                       |
| **Kamailio dies**                            | Your monitoring                                           | —                                                                                         | Nothing new reaches the cluster through it                                                                                                                                  | In production, a second Kamailio takes over the VIP or SRV target (below). In the lab Kamailio is a single point of failure | Manual: [Kamailio loss](#kamailio-loss)                                                      |

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

## Production guidance

- **Kamailio:** run at least two. Either share a virtual IP with keepalived
  (VRRP), so phones keep one address and NAT flows survive a takeover, or
  publish both through DNS SRV for phones that support it. Kamailio is
  stateless apart from its dispatcher view, so either instance serves any
  phone.
- **Valkey:** one primary, at least one replica, and three Sentinels on
  separate hosts (an odd number, so a majority survives one host). Hello uses
  `HELLO_VALKEY_SENTINELS` and `HELLO_VALKEY_MASTER`. Hello needs Valkey 9 or
  later.
- **PostgreSQL:** Hello tolerates PostgreSQL outages for telephony; for
  management availability run PostgreSQL with automatic failover (an operator
  such as CloudNativePG, or Patroni). Phase 6 covers this in the Kubernetes
  guidance.
- **Trusted proxies:** set `HELLO_SIP_TRUSTED_PROXIES` to the Kamailio
  addresses only. Hello trusts the client address and `Path` that Kamailio
  sends, and ignores them from anywhere else.

## Kamailio loss

Not automated in the lab, which runs one Kamailio. To check a production pair:

1. Register two phones and start a call between them.
2. Stop the active Kamailio.
3. Within your VRRP failover time the standby holds the virtual IP; the phones'
   next requests and new calls go through it.
4. Check the Cluster page: both SIP nodes stay READY; no registrations are lost.
