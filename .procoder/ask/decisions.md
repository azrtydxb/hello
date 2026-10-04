# Decisions

## First build scope

- Phase 0 only (foundation), then a separate Phase 1 spec
- Phase 0 + Phase 1 in one milestone

## UI package manager

- npm
- pnpm

## Migration library

- goose
- golang-migrate

## Phase 0 delivery

- Commit on branch phase-0-foundation and open a PR to main
- Commit on the branch only, no PR yet
- Hold — leave uncommitted for review

## Merge PR #1

- Squash-merge now and start the Phase 1 spec
- Hold for your own review

## Phase 1 call model

- B2BUA (signaling only, SDP passed through, media direct)
- Stateful record-routing proxy

## Phase 1 registration store

- Valkey from Phase 1 (cluster-wide, HA-ready)
- In-memory per node until Phase 3

## Phase 1 management API auth

- Local users with sessions and API tokens, no roles yet
- Single bootstrap admin token from env

## Phase 1 automated SIP testing

- Go test user agents built on sipgo, run in CI
- SIPp scenarios in a container

## Phase 1 cross-node delivery to NAT'd phones

- Route via the registering node (Path + flow token, RFC 3327/5626 style)
- Test only without NAT (test phones inside the compose network); defer to Phase 3

## Phase 1 delivery

- Push phase-1-minimum-pbx and open a PR to main
- Hold for your review first

## hello-control readiness during a Valkey outage

- Ready stays green; live views return 503 and /readyz reports Valkey as degraded
- /readyz fails while Valkey is down (all management goes out of rotation)

## Merge PR #2

- Squash-merge now and start the Phase 2 spec
- Hold for your own review

## Phase 2 trunk registration ownership

- One node registers each trunk at a time (Valkey lease, another node takes over on expiry)
- Every node registers the trunk (several contacts at the carrier)

## Phase 2 number rewriting syntax

- Regex match plus replacement template with capture groups, plus simple strip/prefix fields
- Only strip-N-digits and prefix fields

## Phase 2 trunk testing

- Simulated carrier container in the lab, plus a manual check against a real trunk
- Simulated carrier only

## Phase 2 media to carriers

- Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5
- Pull a minimal media relay forward into Phase 2

## Phase 2 delivery

- Push phase-2-trunks-routing and open a PR to main
- Hold for your review first

## Merge PR #3

- Squash-merge now and start the Phase 3 (HA) spec
- Hold for your own review

## Phase 3 how phones reach a surviving node

- UDP load balancer in front of the SIP nodes (lab: nginx stream with health checks), single SIP address for phones
- DNS SRV with both nodes (lab: CoreDNS); relies on phone SRV support
- Both: load balancer by default, SRV documented as the alternative

## Phase 3 Valkey high availability

- Valkey Sentinel (primary, replica, three sentinels) in the lab, with automated failover tests
- Single Valkey; test outage behaviour only, defer Valkey HA to Phase 6 guidance

## Phase 3 PostgreSQL failover

- Test PostgreSQL outage and restart (SIP keeps running from its snapshot); real PG HA left to Phase 6 deployment guidance
- Primary/replica with promotion in the lab now

## Phase 3 production SIP load balancer

- Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/
- A Hello-built Go balancer (hello-lb) shipped as a production component
- Envoy UDP proxy (L4) with active health checks

## Phase 3 delivery

- Push phase-3-ha and open a PR to main
- Hold for your review first

## Merge PR #4

- Squash-merge after CI/Copilot are clean and findings fixed, then start Phase 4 (PBX features)
- Hold for your own review

## Valkey failover test in CI

- Pay for a larger runner (ubuntu-4-cores) for the images job; keep the full failover test gating CI
- Keep the free 2-core runner; the failover test runs pre-merge locally and in CI only as a non-gating scheduled job
- Keep the free runner; the failover test keeps trying to pass in CI with further environment tuning

## Deployment target and order

- Pull Phase 6 (Helm/Kubernetes on kw) forward, right after the Phase 3 merge; features (Phases 4-5) come after
- Keep roadmap order: Phases 4-5 features next; kw deployment stays Phase 6

## Start Phase 4 (PBX features) next

- Yes — draft the Phase 4 spec (transfers, forwarding, DND, ring/hunt groups, voicemail, presence)
- No — I want something else first

## Phase 4 voicemail audio storage

- S3-compatible object storage (MinIO) in the hello namespace
- PostgreSQL bytea/large objects
- Persistent-volume filesystem

## Phase 4 voicemail delivery

- Web + phone (MWI) only; email notification later
- Include SMTP email notification with audio attachment now

## Phase 4 ring/hunt group strategies

- All five now (ring-all, sequential, round-robin, longest-idle, weighted)
- Ring-all + sequential first, the rest later

## Merge Phase 4 (PR #5) and roll out to kw

- Mark PR #5 ready, squash-merge, push images via publish, pin digests, add MinIO+SMTP+feature env to deploy/kuvryn-sync/kw, let Sync roll it out
- Hold PR #5 for review; kw stays on Phase 3 until reviewed
