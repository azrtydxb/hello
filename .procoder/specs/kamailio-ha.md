# kamailio-ha

Status: approved (Option A, VIP 192.168.10.109, 2026-10-26)

Source: the user's instruction of 2026-10-25, after the 09:04 deploy that took the one Kamailio down for about 30 s and dropped an active call (takeover re-INVITEs and the BYE route through it). `docs/ha.md` ("The two named limitations" and "Production guidance") prescribes at least two Kamailio behind a VIP or DNS SRV; kw runs one. This spec is design only: no implementation, no manifest change.

## Problem

Hello survives the loss of any one SIP node, Valkey's primary, PostgreSQL or a control replica, but not of its edge. On kw one Kamailio Deployment (1 replica, `Recreate`) runs with `hostNetwork` on master-11 (`192.168.10.101`), binding `30508` udp and tcp for phones and `5070` udp for Hello (`deploy/kuvryn-sync/kw/resources.yaml`). Every phone is provisioned with `192.168.10.101:30508`. While that pod is down, nothing new reaches the cluster, and the dialogs it record-routed lose in-dialog signalling (re-INVITE, BYE, and the takeover of a call whose node died). A deploy, a node reboot or a crash of master-11 each cause this. Pinning kamailio to a content tag (the `kc-` tag) stops it restarting on unrelated deploys, but does not make it redundant.

## Users

- **Phone users** keep a call through an edge restart, and keep registered phones reachable.
- **The operator** deploys and drains nodes without an outage window.
- **The maintainers** get a design that fits kw's network (Cilium, tunnel mode, kube-proxy replacement, LB-IPAM) and the chart for other clusters.

## Facts the design must respect

1. **Phones point at one address**, `192.168.10.101:30508`, set by provisioning (`KAMAILIO_PUBLIC_HOST`/`PORT`, `HELLO_PROV_*`) and by the phones' stored configuration. It is the node's own address, so it cannot move with the pod.
2. **No NAT between phones and Kamailio.** A NodePort under Cilium SNATs the phones; Kamailio then records the SNAT address as the phone's, and later INVITEs to it are lost. That is why Kamailio is `hostNetwork`.
3. **What names Kamailio in a dialog.** The phone-facing `Record-Route` and the registration `Path` name `PUBLIC_HOST:PUBLIC_PORT` (the `advertise` address). The Hello-facing `Record-Route` names the `hello` socket (`LISTEN_IP:5070`), which is the node address today. After a failover these names must still reach a live Kamailio, or in-dialog requests and takeover re-INVITEs go to a dead address.
4. **Kamailio holds no state Hello needs.** Bindings live in Valkey; dispatcher state is rebuilt by OPTIONS probes within seconds; `tm` transactions in flight are lost on a crash. Either of two identically configured instances can serve any phone and any in-dialog request that carries a `Route`.
5. **Hello trusts Kamailio by address.** `HELLO_SIP_TRUSTED_PROXIES` on kw is the pod CIDR `10.42.0.0/16` plus the node network `192.168.10.0/24`, so a second instance in either needs no change. Hello answers at the source address and port (symmetric), so a replaced instance is probed UP without flushing conntrack.
6. **kw's network:** Cilium with `kube-proxy-replacement`, VXLAN tunnel routing, IPv4 masquerade, socket-LB off (`bpf-lb-sock: false`), LB-IPAM and L2 announcements enabled (`192.168.10.12x`-`15x` already serve LoadBalancer Services), and the BGP control plane enabled. No kube-vip, MetalLB or keepalived runs. Control-plane nodes master-11/12/13 are `192.168.10.101`-`103`; Kamailio is pinned to master-11 by `nodeSelector`.
7. **The init container** waits up to 300 s for `hello-sip-N` DNS and Kamailio fails to start without it, so a restart is about 30 s to minutes. The 2026-10-25 outage was 30 s.

## In scope

- [S-1] A statement of the target: with one Kamailio instance lost (crash, reboot of its node, rollout), registered phones stay reachable and established calls keep in-dialog signalling, with a bounded gap, and new calls succeed through the survivor.
- [S-2] The options below, their trade-offs and a recommendation, so the user can choose.
- [S-3] The work the chosen option needs in the manifests, the Kamailio config, provisioning and the chart, as phases for a later plan.

## Out of scope

- Implementation, manifest or config changes, and changing the phones' provisioned address. Those follow an approved choice.
- Changes to other repositories or to talking-agent's `LoadBalancer` on `192.168.10.142`.
- Making in-flight SIP transactions survive a Kamailio crash; a retransmission (UDP) or a re-REGISTER covers them.
- Kamailio-to-Kamailio dialog or `usrloc` replication (Kamailio keeps nothing Hello needs).

## Options

| #   | Option                                                                                                         | How it works                                                                                                                                                  | Phones keep one address                                     | Source address preserved                                                                          | Failover time                                                                                 | Main costs and risks                                                                                                                                                                                                                                                                                                                                                                                             |
| --- | -------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------- | ------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A   | Two `hostNetwork` Kamailio plus a VRRP VIP (keepalived or kube-vip, ARP mode) on a spare LAN address           | Kamailio on master-11 and master-12 bind `0.0.0.0:30508` and advertise the VIP; the VIP follows a healthy instance                                            | Yes, once phones move from `.101` to the VIP                | Yes (hostNetwork)                                                                                 | VRRP advertisement interval, about 3 s, plus a gratuitous ARP; phones re-use the same address | Needs a new component (none runs on kw) with `NET_ADMIN`/host networking; VRRP multicast must cross the nodes' VLAN; the `hello` socket must also be reachable at an address that survives (see S-3); `ip_nonlocal_bind` is not settable from a pod, so Kamailio binds `0.0.0.0` and the config must tolerate it                                                                                                 |
| B   | Two Kamailio pods behind a Cilium LB-IPAM LoadBalancer IP with L2 announcement, `externalTrafficPolicy: Local` | Same mechanism as the other kw LoadBalancer Services; Cilium moves the L2 lease on node failure                                                               | Yes, once phones move to the LB IP                          | Inbound yes with `Local`; outbound from the pod is masqueraded to the node address, not the LB IP | The L2 lease duration (seconds, tunable)                                                      | Kamailio's own requests (INVITE or BYE to a phone) leave from the node address while `Record-Route` names the LB IP; strict phones may reject it. Needs a lab test, or a Cilium Egress Gateway policy to pin Kamailio's egress to the LB IP. Reverses the reason Kamailio is `hostNetwork`. UDP flows hash to one backend per 5-tuple, so an in-dialog request can land on the other instance (fine, see fact 4) |
| C   | Two `hostNetwork` Kamailio, one address each, phones given DNS SRV                                             | Phones resolve `_sip._udp` to both instances and fail over on their own                                                                                       | No, phones need SRV support (verify per model and firmware) | Yes                                                                                               | Until each phone next re-REGISTERs or times out a transaction, tens of seconds to minutes     | Calls through the dead instance lose in-dialog signalling regardless (its address is in `Record-Route` and `Path`); registrations recover only on the phone's schedule; no new infrastructure                                                                                                                                                                                                                    |
| D   | Anycast over BGP: both Kamailio advertise one /32 through the Cilium BGP control plane                         | The upstream router ECMP-balances between the two nodes; a dead node's route is withdrawn                                                                     | Yes, once phones move to the /32                            | Yes (hostNetwork)                                                                                 | BGP hold time or BFD, seconds                                                                 | Needs the LAN router to speak BGP (unknown); ECMP per-flow hashing and PMTU/fragmentation on UDP; the most moving parts for a home-lab LAN                                                                                                                                                                                                                                                                       |
| E   | Keep one Kamailio, make it restart fast and rarely (the interim)                                               | `kc-` content tag so deploys do not restart it; PodDisruptionBudget cannot help a single replica; shorten the init wait; run it on a node that reboots rarely | Yes (no change)                                             | Yes                                                                                               | None for a crash; 30 s or more for a restart                                                  | Not redundant: a node failure or a Kamailio change still drops calls. It is the baseline this spec improves on, not an alternative to it                                                                                                                                                                                                                                                                         |

Combinations: A or B or D give the address stability; C alone gives only the registration half. E is already delivered.

## Recommendation

**Option A with kube-vip or keepalived, as one VIP on the LAN, two `hostNetwork` Kamailio on master-11 and master-12**, after a lab test of Option B if the user prefers to stay inside Cilium. A keeps the property kw depends on (no NAT, phones un-SNAT-ed, Kamailio sending from the address it advertises), needs no router cooperation, and reuses today's pod spec with a second `nodeSelector`. B is the lighter operational story (everything is Cilium) but contradicts the no-SNAT requirement for Kamailio's own egress until an Egress Gateway policy or a lab test proves it harmless for the phones in use. C is a fallback for phones that cannot be re-pointed. D is for a LAN with a BGP router.

Whichever is chosen:

1. **Both names in a dialog must survive the instance.** The phone-facing `advertise` address is the VIP, so `Path` and the phone-facing `Record-Route` stay valid. The Hello-facing `Record-Route` must also name something that moves: a second VIP, or a ClusterIP Service in front of the two `hello` sockets, since a node address dies with the node. Otherwise a takeover re-INVITE for a call routed through the dead instance still has a dead hop.
2. **The VIP must be provisioned.** Phones, the provisioning server (`KAMAILIO_PUBLIC_HOST`) and `HELLO_SIP_TRUSTED_PROXIES` all carry `192.168.10.101`; moving phones to the VIP is a rolling re-provision (check-sync) in which phones keep working through `.101` until `.101`'s Kamailio is retired.
3. **Roll Kamailio one instance at a time.** `maxUnavailable: 1` with two replicas (hostNetwork, so `Recreate` per pod does not apply; two Deployments, as hello-sip uses, or a StatefulSet), a PodDisruptionBudget, and the same one-at-a-time rule as the SIP nodes. The kw `kc-` content tag keeps even that rare.
4. **Dispatcher state per instance.** Each Kamailio probes hello-sip-1 and hello-sip-2 itself; both converge within the 5 s DNS interval, so no shared state is needed.

## Phases (for the plan that follows an approved choice)

1. **Second instance, no VIP.** A second Deployment on master-12 on a second LAN port or address, same ConfigMap, joined to the PDB and rollout rules. Proves the config and the trusted-proxy range with a test phone pointed at it. No production phone changes.
2. **VIP.** The chosen mechanism, the `advertise` address, the Hello-facing name. Lab-prove a failover with a live call: kill the active Kamailio, assert the call's BYE and a takeover re-INVITE both succeed, and the registered phone receives an INVITE.
3. **Re-point phones.** Provisioning serves the VIP; check-sync the phones; retire `.101` as the phones' address.
4. **Chart and docs.** The chart option for two instances and the VIP; `docs/ha.md` and `docs/kubernetes.md` gain the procedure; the failure table's "Kamailio dies" row changes from "single point of failure" to a tested row.

## Edge cases

- Both Kamailio start together (a node network blip): both bind, the VIP lands on one, and the other serves no phones until it takes the VIP; the dispatcher is correct on both.
- Split brain (VRRP partition): both hold the VIP; phones reach whichever ARP wins. Both instances can serve any phone, so this degrades, not breaks, but Valkey-side registrations are unaffected because Kamailio holds none.
- A phone that cached `.101` and has not re-provisioned keeps using it; that instance must stay up until the last phone moves.
- A call in progress through the instance that fails: phones keep exchanging RTP (the media relay is on the Hello node, not Kamailio); only in-dialog signalling needs the survivor, and it works if the Record-Route names the VIP.
- TCP phones (port 30508 tcp) lose their connection on failover and re-establish it to the VIP.

## Failure modes

- VIP never moves (the mechanism pod is unhealthy): the active Kamailio stays the only edge; monitor the VIP holder and alert when no node holds it.
- Both instances unreachable: unchanged from today (no new calls), with a cluster-level alert.
- The second instance diverges in config: the ConfigMap is shared and the config checksum annotation rolls both; `test/deploy` already compares the ConfigMap with `deploy/kamailio/kamailio.cfg`.

## Acceptance criteria

- With a call established through the edge, deleting the active Kamailio pod (or powering off its node) leaves the call's media running, a BYE from either phone ends the call within 5 s of the VIP moving, and a takeover of the call's node during the outage completes after it.
- A phone registered before the failover receives an INVITE after it, with no re-REGISTER.
- A rolling change to the Kamailio manifest never leaves both instances down (checked by a `test/deploy` invariant like the one for hello-sip).
- `docs/ha.md`'s Kamailio row names the test that proves it.

## Open questions

1. Is a free LAN address available for a VIP, and which? (The `192.168.10.12x`-`15x` block is Cilium LB-IPAM's; a VRRP VIP should be outside it.)
2. May phones be re-provisioned to a new proxy address, and are all WP822/WP836 reachable for check-sync today? That decides how long `.101` has to stay up.
3. Does the LAN router speak BGP? Only Option D needs it.
4. Is a Cilium Egress Gateway acceptable on kw? Only Option B needs it; Option A does not touch Cilium.
5. Are any of the phones in use unable to use DNS SRV (Option C)?
