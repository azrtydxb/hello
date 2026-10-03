# What a human decided

Written 2026-10-03 08:28 UTC. procoder reads this
file to avoid asking a question twice; edit an answer here to change what
it believes. Reword the question and it will be asked again.

## [decision] decisions.md

Key: 06e6d3e2170c
Question: Phase 2 trunk testing

- Simulated carrier container in the lab, plus a manual check against a real trunk
- Simulated carrier only

Answer: Simulated carrier container in the lab, plus a manual check against a real trunk

## [decision] decisions.md

Key: 07cc11272355
Question: Migration library

- goose
- golang-migrate

Answer: goose

## [decision] decisions.md

Key: 0bd764b7a070
Question: Phase 3 Valkey high availability

- Valkey Sentinel (primary, replica, three sentinels) in the lab, with automated failover tests
- Single Valkey; test outage behaviour only, defer Valkey HA to Phase 6 guidance

Answer: Valkey Sentinel (primary, replica, three sentinels) in the lab, with automated failover tests

## [decision] decisions.md

Key: 17469bfde72e
Question: Phase 1 automated SIP testing

- Go test user agents built on sipgo, run in CI
- SIPp scenarios in a container

Answer: Go test user agents built on sipgo, run in CI

## [decision] decisions.md

Key: 1a17adfb2c72
Question: hello-control readiness during a Valkey outage

- Ready stays green; live views return 503 and /readyz reports Valkey as degraded
- /readyz fails while Valkey is down (all management goes out of rotation)

Answer: Ready stays green; live views return 503 and /readyz reports Valkey as degraded

## [decision] decisions.md

Key: 1fc2454e9927
Question: UI package manager

- npm
- pnpm

Answer: pnpm

## (no longer asked)

Key: 2a0645102517
Question: OPEN: UI package manager — npm or pnpm?

Answer: pnpm

## [decision] decisions.md

Key: 2de9213b4811
Question: Phase 2 media to carriers

- Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5
- Pull a minimal media relay forward into Phase 2

Answer: Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5

## [decision] decisions.md

Key: 34cff7a7f99a
Question: Merge PR #1

- Squash-merge now and start the Phase 1 spec
- Hold for your own review

Answer: Squash-merge now and start the Phase 1 spec

## [decision] decisions.md

Key: 371125ececfd
Question: Merge PR #3

- Squash-merge now and start the Phase 3 (HA) spec
- Hold for your own review

Answer: Squash-merge now and start the Phase 3 (HA) spec

## [decision] decisions.md

Key: 406a5e21d741
Question: Phase 2 trunk registration ownership

- One node registers each trunk at a time (Valkey lease, another node takes over on expiry)
- Every node registers the trunk (several contacts at the carrier)

Answer: One node registers each trunk at a time (Valkey lease, another node takes over on expiry)

## [decision] decisions.md

Key: 4852be1d915c
Question: Phase 1 call model

- B2BUA (signaling only, SDP passed through, media direct)
- Stateful record-routing proxy

Answer: B2BUA (signaling only, SDP passed through, media direct)

## [decision] decisions.md

Key: 4c87c8814e53
Question: First build scope

- Phase 0 only (foundation), then a separate Phase 1 spec
- Phase 0 + Phase 1 in one milestone

Answer: all — build the full roadmap (Phases 0-7) in phase order; Phase 0 first, one spec + milestone per phase.

## (no longer asked)

Key: 5470505a44c0
Question: OPEN: Scope of the first build — Phase 0 only, or Phase 0 plus Phase 1 (SIP/UDP REGISTER, digest auth, internal calls between two phones) in the same milestone?

Answer: all — build the full roadmap (Phases 0-7) in phase order; Phase 0 first, one spec + milestone per phase.

## (no longer asked)

Key: 61fa8fc32239
Question: OPEN: Migration library — goose, or golang-migrate?

Answer: goose

## [decision] decisions.md

Key: 70635c04fd67
Question: Phase 2 number rewriting syntax

- Regex match plus replacement template with capture groups, plus simple strip/prefix fields
- Only strip-N-digits and prefix fields

Answer: Regex match plus replacement template with capture groups, plus simple strip/prefix fields

## [decision] decisions.md

Key: 7d9ccebf04dd
Question: Phase 1 delivery

- Push phase-1-minimum-pbx and open a PR to main
- Hold for your review first

Answer: Push phase-1-minimum-pbx and open a PR to main

## [decision] decisions.md

Key: 8e6949c72bfe
Question: Merge PR #2

- Squash-merge now and start the Phase 2 spec
- Hold for your own review

Answer: Squash-merge now and start the Phase 2 spec

## [decision] decisions.md

Key: 8efe37eb2d42
Question: Phase 1 management API auth

- Local users with sessions and API tokens, no roles yet
- Single bootstrap admin token from env

Answer: Local users with sessions and API tokens, no roles yet

## [decision] decisions.md

Key: 8f478deba27c
Question: Phase 0 delivery

- Commit on branch phase-0-foundation and open a PR to main
- Commit on the branch only, no PR yet
- Hold — leave uncommitted for review

Answer: Commit on branch phase-0-foundation and open a PR to main

## [decision] decisions.md

Key: a29492463871
Question: Phase 3 production SIP load balancer

- Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/
- A Hello-built Go balancer (hello-lb) shipped as a production component
- Envoy UDP proxy (L4) with active health checks

Answer: Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/ (the user requires a production-grade balancer, not lab tooling)

## [decision] decisions.md

Key: bc412d83b326
Question: Phase 2 delivery

- Push phase-2-trunks-routing and open a PR to main
- Hold for your review first

Answer: Push phase-2-trunks-routing and open a PR to main

## [decision] decisions.md

Key: c9c0a0c47220
Question: Phase 3 how phones reach a surviving node

- UDP load balancer in front of the SIP nodes (lab: nginx stream with health checks), single SIP address for phones
- DNS SRV with both nodes (lab: CoreDNS); relies on phone SRV support
- Both: load balancer by default, SRV documented as the alternative

Answer: UDP load balancer in front of the SIP nodes (lab: nginx stream with health checks), single SIP address for phones

## [decision] decisions.md

Key: cbac333e0ce9
Question: Phase 3 delivery

- Push phase-3-ha and open a PR to main
- Hold for your review first

Answer: Push phase-3-ha and open a PR to main

## [decision] decisions.md

Key: d774f897f39a
Question: Phase 3 PostgreSQL failover

- Test PostgreSQL outage and restart (SIP keeps running from its snapshot); real PG HA left to Phase 6 deployment guidance
- Primary/replica with promotion in the lab now

Answer: Test PostgreSQL outage and restart (SIP keeps running from its snapshot); real PG HA left to Phase 6 deployment guidance

## [decision] decisions.md

Key: e776054b2093
Question: Phase 1 registration store

- Valkey from Phase 1 (cluster-wide, HA-ready)
- In-memory per node until Phase 3

Answer: Valkey from Phase 1 (cluster-wide, HA-ready)

## [decision] decisions.md

Key: f9afd6b87078
Question: Phase 1 cross-node delivery to NAT'd phones

- Route via the registering node (Path + flow token, RFC 3327/5626 style)
- Test only without NAT (test phones inside the compose network); defer to Phase 3

Answer: Route via the registering node (Path + flow token, RFC 3327/5626 style)
