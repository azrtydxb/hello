# Hello on Kubernetes

Hello installs on Kubernetes from one Helm chart, `deploy/helm/hello`. It
renders hello-control (two replicas, migrations as an init container),
hello-sip (N nodes), Kamailio (the SIP edge), the console with an optional
Ingress, and, for evaluation only, single-instance PostgreSQL, Valkey and
MinIO. This page covers installing it, what SIP and RTP need from your
network, how pods are spread and protected, how to run PostgreSQL and Valkey
highly available, and how to upgrade without dropping calls. For what the
cluster guarantees when a node fails, see [docs/ha.md](ha.md).

The chart is the install path for any cluster. The project's own cluster
(kw) does not use it: it runs from raw manifests applied by Kuvryn Sync
(`deploy/kuvryn-sync/kw/resources.yaml`), which stay the source of truth the
chart is tested against (see [How kw differs](#how-kw-differs)).

## Install

Requirements: Kubernetes 1.27 or later, Helm 3.14 or later (4.x works), a
node address phones can reach for Kamailio, and the images (the CI of this
repository publishes `hello-control`, `hello-sip`, `hello-ui` and `kamailio`
to `ant.azrty.com/oci-hosted/azrtydxb`, which kw pulls from its Ant replica
`ant.kw.watteel.lab/oci-hosted/azrtydxb`, the chart default; mirror them and
set `global.imageRegistry` for any other registry).

1. Create the credentials Secret. The chart never creates or reads
   credentials from values; it only references this Secret
   (`secrets.existingSecret`):

   ```sh
   kubectl create namespace hello
   kubectl -n hello create secret generic hello-credentials \
     --from-literal=dsn='postgres://hello:CHANGE-ME@hello-hello-postgresql:5432/hello?sslmode=disable' \
     --from-literal=secretKey="$(openssl rand -base64 32)" \
     --from-literal=nonceSecret="$(openssl rand -base64 48)" \
     --from-literal=bootstrapPassword='CHANGE-ME-first-admin' \
     --from-literal=minioAccessKey=hello \
     --from-literal=minioSecretKey="$(openssl rand -base64 24)" \
     --from-literal=postgresUser=hello \
     --from-literal=postgresPassword='CHANGE-ME'
   ```

   `postgresUser` and `postgresPassword` are only read by the bundled
   PostgreSQL. The AI agent's keys (`provider`, `base-url`, `model`,
   `api-key`) are optional: without them the agent stays off.

2. Evaluate everything on one cluster (bundled state services, no
   replication, no backups):

   ```sh
   helm install hello deploy/helm/hello -n hello \
     -f deploy/helm/hello/values-eval.yaml \
     --set global.sipDomain=hello.example.com \
     --set kamailio.publicHost=192.168.10.101 \
     --set kamailio.nodeSelector."kubernetes\.io/hostname"=node-a
   ```

   Three values have no default and the chart refuses to render without
   them: `global.sipDomain`, `kamailio.publicHost` and
   `secrets.existingSecret`. A Valkey and a MinIO must be either bundled or
   external; a missing one fails the render with a message naming the value.

3. Production: copy `values-ha.yaml`, replace the site-specific names and
   pin an immutable image tag (`sha-<commit>`; `main` moves):

   ```sh
   helm install hello deploy/helm/hello -n hello -f my-values.yaml
   ```

Sign in as `admin` with the `bootstrapPassword` and change it.

The UI's nginx re-resolves `hello-control` at request time and needs a DNS
resolver for that. `ui.dnsResolver` defaults to empty, and the UI container
then uses the first `nameserver` in its own `/etc/resolv.conf`, which is the
cluster DNS on any cluster whatever its service CIDR. Set `ui.dnsResolver`
(IPv6 addresses in brackets, `[fd00::a]`) only to force a different resolver.

## SIP networking

SIP and media are the part of Hello that does not behave like a web
application on Kubernetes, because the addresses inside the messages matter.
Three things must be reachable:

| Traffic                          | From                | To                                  | Ports                                   |
| -------------------------------- | ------------------- | ----------------------------------- | --------------------------------------- |
| Registration and call signalling | Phones and carriers | Kamailio                            | UDP and TCP `kamailio.publicPort`       |
| Anchored media (RTP)             | Phones and carriers | The hello-sip node that owns a call | UDP 20000-21000 per node (configurable) |
| Console, API, provisioning       | Browsers and phones | The Ingress                         | TCP 443 (provisioning also 80)          |

Inside the cluster, Kamailio talks to the hello-sip nodes on UDP 5060 (and
their HTTP port 8082 for health), the nodes talk to Kamailio's "hello"
socket on UDP 5070, and everything reaches PostgreSQL, Valkey and MinIO.
Never expose 5070: it is the trusted side of the edge.

### hostNetwork (recommended)

`kamailio.hostNetwork: true` and `sip.hostNetwork: true` (both the default)
bind Kamailio's phones port, hello-sip's SIP port and the RTP range on the
node's own address:

- phones reach Kamailio with no NAT in between, so Kamailio sees their real
  source address, and the in-dialog requests it later sends to them reach
  them (through a NodePort, many CNIs translate the source, Kamailio records
  the translated address as the phone's, and later INVITEs to it are lost);
- no Service can map a range of 1,000 UDP ports, so anchored media can only
  reach the node address; `sip.mediaAnchorHost: hostIP` stamps the address
  of the node a pod runs on into its SDP answers.

Consequences: one hello-sip pod per Kubernetes node (the required
anti-affinity enforces it, since two would fight over UDP 5060 and TCP
8082), Kamailio pinned to the node whose address is `kamailio.publicHost`
(`kamailio.nodeSelector`), and node-level firewalls (security groups) must
allow the ports above. The chart's pods keep cluster DNS
(`ClusterFirstWithHostNet`) so they still resolve Services.

If that node is lost, Kamailio is not rescheduled to another address by
itself. Put `publicHost` on a virtual IP that moves between a few edge
nodes (kube-vip, keepalived, a cloud load balancer with UDP) and label those
nodes; Kamailio redundancy itself is a documented limitation, see
[docs/ha.md](ha.md#kamailio-loss).

### NodePort or LoadBalancer

With `kamailio.hostNetwork: false` the chart creates a Service for the
phones' port (`kamailio.service.type`: `LoadBalancer` or `NodePort`, with
`externalTrafficPolicy: Local` by default so the source address survives):

```sh
helm install hello deploy/helm/hello -n hello -f values-eval.yaml \
  --set kamailio.hostNetwork=false \
  --set kamailio.service.type=NodePort --set kamailio.service.nodePort=30508 \
  --set kamailio.publicPort=30508 --set kamailio.phonesPort=5060 \
  --set kamailio.publicHost=192.168.10.101
```

`publicHost` and `publicPort` are what phones are told in Via and
Record-Route, so they must be the address and port phones really use
(the node address and nodePort, or the load balancer's address and port).
`phonesPort` is what Kamailio binds inside its pod. Limits to know:

- A load balancer or kube-proxy that SNATs hides every phone behind one
  source address. Kamailio's NAT handling copes, but per-IP flood protection
  (`KAMAILIO_PIKE_DENSITY`) and the failed-authentication throttle then act
  on the shared address; keep `externalTrafficPolicy: Local` and check that
  your load balancer preserves the client address.
- A UDP load balancer must support mixed UDP and TCP on one port or you
  serve UDP only.
- Media does not follow: hello-sip still needs a routable address.
  `sip.hostNetwork: false` with `sip.mediaAnchorHost: podIP` works only when
  the pod network is routable from the phones (native routing, BGP), and a
  literal address in `sip.mediaAnchorHost` is the same for every node. With
  more than one node, use `hostIP`, or accept that only calls whose media
  does not need the anchor work.

### RTP port ranges

Each hello-sip node draws RTP ports from `sip.rtpPortMin`-`sip.rtpPortMax`
(default 20000-21000: the range holds a limited number of simultaneous
anchored calls per node, each leg taking a port; size it for your peak). The range must be open for UDP from
the phones' and carriers' networks to every node, and not used by anything
else on those nodes. When it is exhausted a call falls back to direct media
and the failure is counted ([docs/media.md](media.md)). Keep the range
identical across nodes; it is one value for the StatefulSet.

### Advertised addresses and NAT

- Phones learn Kamailio's address from `kamailio.publicHost` and
  `kamailio.publicPort`. Use a stable name or VIP, never a pod address.
- hello-sip nodes advertise their stable pod DNS name
  (`hello-sip-0.hello-sip.<namespace>.svc.<clusterDomain>`), which is also the
  name in Kamailio's dispatcher list. This is why hello-sip is a
  StatefulSet behind a headless Service: the name survives restarts, and a
  ClusterIP in front of the nodes would let socket-level load balancing
  (Cilium socket-LB) terminate Kamailio's UDP socket when a backend goes.
- `sip.mediaAnchorHost` is what the anchor tells peers to send RTP to.
  Behind NAT it must be the address the peers can reach: with `hostIP`
  that is the node's address on the phones' network. Public carriers
  behind a 1:1 NAT need each node's public address advertised, which one
  StatefulSet value cannot express; terminate carriers on a separate
  edge or give nodes publicly routable addresses.
- Phones behind NAT of their own are handled by Kamailio and the anchor
  (NAT detection, keep-alives); see [docs/media.md](media.md).
- `sip.trustedProxies` (`HELLO_SIP_TRUSTED_PROXIES`) lists the source
  addresses allowed to set Path and `X-Hello-Client`: Kamailio's pod CIDR,
  plus the node CIDR when Kamailio is on the host network (the pod network
  may masquerade traffic between pods on the same node). Narrow the default
  `10.0.0.0/8` to your CIDRs; trusting more than Kamailio's addresses lets
  anyone in that range spoof a phone's address.

## Placement and disruption

- **hello-sip** carries a required `podAntiAffinity` on
  `kubernetes.io/hostname` against its own pods: one node per Kubernetes
  node, so one machine failing takes down at most one SIP node. With fewer
  Kubernetes nodes than `sip.nodes`, the extra pods stay Pending. Set
  `sip.antiAffinity: preferred` only without hostNetwork. For zones, add
  `sip.topologySpreadConstraints` on `topology.kubernetes.io/zone`.
- **hello-control** prefers to spread (`control.antiAffinity: preferred`),
  so a small cluster still starts; set `required` where you have the nodes.
- **PodDisruptionBudgets** keep a voluntary disruption (a node drain, a
  cluster autoscaler, an upgrade of the nodes) from removing the last ready
  node: hello-sip allows one unavailable at a time, which leaves the other
  as the survivor the first hands its calls to; hello-control and hello-ui
  (when they have more than one replica) the same. A single hello-sip node
  gets no budget, since it would block every drain forever. Kamailio is one
  replica and has none for the same reason.
- Draining a Kubernetes node with `kubectl drain` evicts a hello-sip pod,
  which receives SIGTERM, drains (its calls move to another node) and exits
  inside `sip.terminationGracePeriodSeconds` (default 150 s, above
  `sip.drainTimeoutSeconds`, 90 s; the chart refuses a drain longer than the
  grace period).
- Resources: the defaults match what runs on kw. Size
  `sip.resources` by concurrent calls; memory is the limit that matters for
  the anchor.

## Highly available state

The chart's bundled PostgreSQL, Valkey and MinIO are single instances without
replication or backup, for evaluation. For production, switch them off and
point the chart at services that are highly available themselves
(`values-ha.yaml` shows the layout).

### PostgreSQL

Hello needs one writable PostgreSQL (version 18 is what CI tests), reached
through the `dsn` in the credentials Secret. Telephony tolerates a PostgreSQL
outage ([docs/ha.md](ha.md)); management does not, so run it with automatic
failover:

- **CloudNativePG** (recommended on Kubernetes): a `Cluster` with three
  instances across nodes or zones, synchronous replication for no data
  loss, and the read-write Service as the target:

  ```yaml
  apiVersion: postgresql.cnpg.io/v1
  kind: Cluster
  metadata:
    name: hello-pg
    namespace: databases
  spec:
    instances: 3
    storage:
      size: 20Gi
    postgresql:
      synchronous:
        method: any
        number: 1
    affinity:
      enablePodAntiAffinity: true
      topologyKey: kubernetes.io/hostname
    bootstrap:
      initdb:
        database: hello
        owner: hello
  ```

  The DSN is `postgres://hello:<password>@hello-pg-rw.databases:5432/hello?sslmode=require`.
  Add continuous backup to object storage (`backup.barmanObjectStore`) and
  test a restore; high availability is not a backup.

- **A managed service** (RDS, Cloud SQL, Azure Database): use its
  multi-AZ failover endpoint and `sslmode=require` or stricter.
- Whichever you choose: the migration runs in hello-control's init container
  with the same DSN, so the role must be allowed to create and alter its own
  schema; failover takes seconds, and hello-control reconnects.

### Valkey with Sentinel

Hello keeps registrations, call state and cluster membership in Valkey 9 or
later, and follows a Sentinel-managed primary. Set
`valkey.external.sentinels` and `valkey.external.master` (the chart then sets
`HELLO_VALKEY_SENTINELS` and `HELLO_VALKEY_MASTER`; a single address,
`valkey.external.addr`, is for evaluation). Guidance, from
[docs/ha.md](ha.md#production-guidance):

- one primary and at least one replica, three Sentinels with quorum 2, on
  different nodes or zones (a StatefulSet with anti-affinity for each,
  or a chart such as a Valkey Sentinel Helm chart that does the same);
- `down-after-milliseconds` about 5000; promotion then completes in about
  five seconds;
- `sentinel resolve-hostnames yes` and `sentinel announce-hostnames yes`, with
  each Valkey and Sentinel announcing its stable pod DNS name, because pod
  addresses change when pods are replaced;
- replication is asynchronous, so a failover can lose the last writes
  (a registration or call state written just before the primary died);
- while no primary is known, a node reports not ready and Kamailio stops
  sending it work, then recovers within about 15 seconds of the promotion.

### MinIO

Voicemail and recordings live in an S3-compatible store. Point
`minio.external.endpoint` at your object store (set `minio.external.secure`
for TLS); any S3 API with the credentials in the Secret works. Use the
store's own durability (erasure coding or replication across zones).

## Upgrades

hello-sip is a StatefulSet with `podManagementPolicy: OrderedReady` and the
`RollingUpdate` strategy, so `helm upgrade` replaces one node at a time,
highest ordinal first, and does not touch the next until the replaced node
is ready and has stayed ready for `sip.minReadySeconds` (default 45 s). That
wait is deliberately conservative: Kamailio learns a replaced pod's new
address from cluster DNS, whose answers CoreDNS caches for 30 s by default
and Kamailio refreshes every 5 s, so for that long it may neither trust
requests from the new address nor send to it, and a drain that hands its
calls over earlier could hand them to a node Kamailio cannot yet reach. If your cluster DNS uses a shorter
TTL you may lower it. Each node receives SIGTERM, drains
and hands its calls to a node that is ready, exits, and its replacement
joins. With two or more nodes there is always a ready survivor; a single
node has none, and its calls end during an upgrade.

1. Read the release notes; check that every hello-sip node is READY on the
   Cluster page, and that PostgreSQL and Valkey are healthy.
2. Upgrade Hello's own pods and keep Kamailio where it is:

   ```sh
   helm upgrade hello deploy/helm/hello -n hello -f my-values.yaml \
     --set global.imageTag=sha-<new commit> \
     --set kamailio.image.tag=sha-<current kamailio commit> \
     --wait --timeout 20m
   ```

   hello-control (two replicas) rolls with `maxUnavailable: 0`, running the
   migration in each new pod first; hello-ui rolls the same way. Concurrent
   migrations serialise on a PostgreSQL advisory lock, so only one new pod
   applies a migration; read the release notes for a migration the old
   pods cannot run against.

3. Upgrade Kamailio when its image or configuration changes (its config
   checksum annotation rolls the pod when `kamailio.cfg` or the node list
   changes). Kamailio is one replica with the `Recreate` strategy (under
   hostNetwork the new pod needs the old one's ports), so signalling pauses
   for the seconds it is down. Established calls keep their media
   (anchored RTP is carried by the hello-sip nodes); in-dialog requests
   made while it is down (a hangup, a hold) fail or retry, and a handoff
   in progress at that moment would fail too, which is why this step is not
   combined with step 2. Kamailio's Record-Route for the hello nodes carries
   its own pod address, so on the pod network (hostNetwork off) a replaced
   Kamailio has a new address, and requests that hello-sip originates inside
   calls set up before the roll (a hangup by the far side, a re-INVITE) go to
   the old one and are lost; the kind job shows this, and only a phone's own
   hangup still works. With hostNetwork the address is the node's and survives
   the roll, which is one more reason it is the recommended mode. The
   wait-dispatcher init container makes the new
   Kamailio wait (bounded) until the nodes' names resolve, instead of
   crash-looping.
4. Verify: the Cluster page shows every node READY on the new version, the
   calls that were active are still listed (a call that moved shows
   `ha: taken-over` in the live view and its CDR trace), and
   `hello_zombie_calls_total` has not moved.

What a caller experiences: a hello-sip node drain moves the call by
re-INVITE; the audio gap is the re-INVITE round trip, milliseconds, because
the draining node relays until then ([docs/ha.md](ha.md)). The CI job
`kind-upgrade` in `.github/workflows/ci.yaml` runs this: it installs the
chart on a kind cluster, connects a call through Kamailio, upgrades with a
changed image tag and asserts the call survives and can be hung up from
both sides.

Rollback is `helm rollback hello`. It is the same rolling procedure in
reverse and equally safe for calls; a database migration is not rolled back,
so upgrade across migrations forward only.

If an upgrade stalls on a node that does not become ready, the remaining
nodes keep serving and the rollout waits; `kubectl -n hello logs
hello-sip-<n>` and `kubectl -n hello describe pod hello-sip-<n>` show why.
Pods Pending for lack of a node (anti-affinity) is the usual cause of a
three-node install on a two-node cluster.

## How kw differs

kw (the project's own cluster) deploys from
`deploy/kuvryn-sync/kw/resources.yaml` through Kuvryn Sync, never through
Helm: a `helm upgrade` or a manual `kubectl apply` beside Sync fights the
field manager. The chart keeps kw's decisions and changes how they are
expressed:

| Concern           | kw (raw manifests)                                                              | Chart                                                          |
| ----------------- | ------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| hello-sip nodes   | Deployments `hello-sip-1`, `hello-sip-2`, one headless Service each, `Recreate` | One StatefulSet, one headless Service, ordered `RollingUpdate` |
| Staggered rollout | Rollout gate: a stopping node takes a Valkey token and waits for a READY peer   | Inherent: the next pod is not replaced until the last is ready |
| Node identity     | Pod name of a Deployment; advertised Service name                               | Stable pod name; advertised pod DNS name                       |
| Kamailio          | Pinned to master-11, ports 30508/9071                                           | `hostNetwork` option, `nodeSelector`, ports are values         |
| Secrets           | Five Sync-managed SOPS Secrets                                                  | One existing Secret by reference                               |
| State services    | PostgreSQL, MinIO, two Valkeys, three Sentinels as Deployments                  | Optional bundled single instances, or external                 |

`test/deploy` renders the chart and compares it with the kw manifests: every
environment variable kw sets on hello-sip, hello-control and Kamailio must
exist in the chart (the few kw-only tuning variables are listed in the test
with their reasons), probes and ports must match, and the Kamailio
configuration must equal `deploy/kamailio/kamailio.cfg`.
