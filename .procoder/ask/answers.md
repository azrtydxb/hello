# What a human decided

Written 2026-10-08 14:27 UTC. procoder reads this
file to avoid asking a question twice; edit an answer here to change what
it believes. Reword the question and it will be asked again.

## [decision] decisions.md

Key: 011dba9bcb59
Question: Phase 7 in-call HA scope (after the anchored-vs-direct explanation)

- In-call HA for anchored calls only; NAT'd direct-media calls stay best-effort
- Always-on anchoring: every call anchored so all calls get in-call HA
- In-call HA for anchored calls + policy change: LAN-to-LAN calls also anchor (cheap on a LAN, makes every call survivable)
- Defer Phase 7; stop at the current scope

Answer: Anchor everything + full HA (all calls anchor, LAN-to-LAN included; reverses Phase 5 conditional anchoring deliberately)

## (no longer asked)

Key: 01f93861d03b
Question: OPEN: Auditing MCP reads. "Every call is audited like a user call": a user's reads write no audit row today. Should MCP read tool calls write audit rows too, or is a log line plus metrics per call (and the normal audit rows for every change) enough? Proposed: log line plus metrics for reads, audit rows for changes, as for users.

Answer: Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)

## [decision] decisions.md

Key: 0562d75801e2
Question: Start Phase 4 (PBX features) next

- Yes — draft the Phase 4 spec (transfers, forwarding, DND, ring/hunt groups, voicemail, presence)
- No — I want something else first

Answer: Yes — draft the Phase 4 spec

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

## [spec] voice-agents

Key: 0dbd568da15e
Question: 7. **Agents in ring groups.** The spec forbids an agent in `ring-all`/`longest-idle`/`weighted` groups and recommends it as the failure target. Do you also want it as a normal `sequential` member (for example "ring the desk phone, then the assistant"), or only as a failure target?

Answer: Sequential member and failure target both allowed (sequential and round-robin as in the spec).

## [decision] decisions.md

Key: 10ecd9488531
Question: Phase 5 announcements

- In scope: named announcement sets played on demand (failure destinations, before transfer)
- Out of scope for Phase 5

Answer: In scope: named announcement sets played on demand (failure destinations, before transfer)

## [decision] decisions.md

Key: 17469bfde72e
Question: Phase 1 automated SIP testing

- Go test user agents built on sipgo, run in CI
- SIPp scenarios in a container

Answer: Go test user agents built on sipgo, run in CI

## [decision] decisions.md

Key: 18471647d238
Question: Phone auto-provisioning: token hand-off for DHCP-discovered phones

- Trust on first use: an allowlisted, never-fetched MAC gets its token URL once via the boot path (optionally only from configured CIDRs), then the boot path closes for that MAC until re-armed; a second claim is flagged
- DHCP bootstraps only (CA, re-check, "not provisioned"); credentials only via redirect service or typed URL
- MAC-only on the boot path from trusted CIDRs, no token (weakest; not recommended)

Answer: Trust on first use: the first fetch from a pre-registered MAC hands out its token once; an administrator can re-arm

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

## [spec] voice-agents

Key: 226906a02b58
Question: 3. **MCP credentials beyond bearer/header.** Real MCP servers use OAuth 2.1. Is a static token or header enough for the servers you will attach (for example Hello's own MCP server, home-automation, calendar), or must a voice agent act through OAuth on behalf of a user (a much bigger design: token refresh, per-user consent, caller identity)?

Answer: Both: static bearer/header tokens and OAuth 2.1 client-credentials (for example a Hello service account), all credentials sealed in Hello.

## [decision] decisions.md

Key: 237b6f7b23a7
Question: AI phase 1: user roles for OAuth consent

Hello has one kind of user, who can do everything, so the role that should bound the scopes a user can grant bounds nothing today (spec ai-external-access, S-5).

- Keep one role in phase 1: every user may grant every scope, `secrets` included; `GrantableScopes` is where a later roles spec plugs in (proposed)
- Add roles now (for example administrator, operator, read-only), applied to the console and the API as well as to consent

Answer: Add roles now: viewer (read-only), operator (day-to-day configuration writes), admin (users, roles, tokens, secrets, OAuth clients, settings); existing users become admin; the console shows and edits a user's role; the API enforces a minimum role on every route; consent, MCP scopes and service accounts are bounded by role

## [spec] voice-agents

Key: 2567471bf9c0
Question: 6. **Licence of talking-agent and Breeze.** `LICENSE.md` contains only `ok`; Breeze TTS 2 weights are research-only. Which licence should talking-agent have, and is any commercial or customer use of the lab voice intended before a commercial TTS is chosen?

Answer: Plaintext SIP/RTP with the HMAC-signed INVITE is acceptable on kw; TLS/SRTP is a later option.

## (no longer asked)

Key: 2a0645102517
Question: OPEN: UI package manager — npm or pnpm?

Answer: pnpm

## [decision] decisions.md

Key: 2ad55e86b114
Question: Phase 4 voicemail audio storage

- S3-compatible object storage (MinIO) in the hello namespace
- PostgreSQL bytea/large objects
- Persistent-volume filesystem

Answer: S3-compatible object storage (MinIO) in the hello namespace

## [decision] decisions.md

Key: 2de9213b4811
Question: Phase 2 media to carriers

- Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5
- Pull a minimal media relay forward into Phase 2

Answer: Direct media (phone to carrier) as the roadmap says; anchoring waits for Phase 5

## [decision] decisions.md

Key: 3322f2afa6d0
Question: Remaining items after Phase 7 (2026-10-06)

- Phone auto-provisioning vendors: Yealink, Poly, Grandstream, Snom/Fanvil, and any brand (generic per-model templates)
- Provisioning discovery: DHCP option 66, vendor cloud redirect, and manual URL entry — all three
- Provisioning auth: MAC + per-device token URL over HTTPS
- Load tests (§27): later, when the user gives a window
- SMTP relay on kw: leave voicemail email off
- Real carrier trunk check: not yet, stays pending
- Emblem: the user dropped the full design export in the repo root; use its assets, then delete the folder

Answer: Vendors: Yealink, Poly, Grandstream, Snom, Fanvil plus any brand via generic per-model templates; discovery: DHCP option 66 + vendor redirect + manual URL; auth: MAC + per-device token URL over HTTPS; load tests: later; SMTP on kw: voicemail email off; real trunk check: not yet; emblem: use the dropped design assets (done in PR #24)

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

Key: 38da7c550e31
Question: Phone auto-provisioning: phone admin (web UI) password

- Random per phone, sealed, revealable to administrators (audited)
- One site-wide password the user sets
- Leave the phones' admin password untouched

Answer: Random per phone, stored encrypted (sealed), revealable to administrators (audited)

## [decision] decisions.md

Key: 406a5e21d741
Question: Phase 2 trunk registration ownership

- One node registers each trunk at a time (Valkey lease, another node takes over on expiry)
- Every node registers the trunk (several contacts at the carrier)

Answer: One node registers each trunk at a time (Valkey lease, another node takes over on expiry)

## [decision] decisions.md

Key: 43202daa3527
Question: AI phase 1: dynamic client registration on kw

Client ID metadata documents are preferred and DCR is deprecated, but some MCP clients still only register dynamically (spec ai-external-access, S-10).

- On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)
- Off: only client ID metadata documents and service accounts

Answer: On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)

## [decision] decisions.md

Key: 451638dd32f2
Question: Phone auto-provisioning: vendor redirect accounts

- Automate Snom SRAPS and Yealink RPS (the user supplies API credentials); Grandstream GDMS add-device with serials; Poly and Fanvil stay a documented manual step
- Also use the by-request Poly ZTP XML API and Fanvil's undocumented FDPS XML-RPC
- No redirect accounts for now; DHCP and manual entry only, clients built and tested against fakes

Answer: Wire and live-test Snom SRAPS, Yealink RPS/YMCS and Grandstream GDMS on kw (credentials arrive later as sops secrets; live tests gated on them existing); Poly and Fanvil stay a manual step

## [spec] voice-agents

Key: 457b17a5bdc0
Question: 9. **Where limits are enforced.** Concurrency is enforced by talking-agent (`486`). Should Hello also cap simultaneous agent calls per agent or globally (it knows the calls and could refuse before sending an INVITE), accepting that two sources then hold the number?

Answer: Hello caps calls per agent and in total (busy sends the call to the failover destination); talking-agent also refuses with 486 when its speech models are saturated.

## [decision] decisions.md

Key: 4852be1d915c
Question: Phase 1 call model

- B2BUA (signaling only, SDP passed through, media direct)
- Stateful record-routing proxy

Answer: B2BUA (signaling only, SDP passed through, media direct)

## [spec] voice-agents

Key: 48b95c74d6ea
Question: 2. **Transcripts.** Default is summaries only, transcripts off per agent and readable by admins only, 30 days. Is that right for your use, and do you need transcripts at all (recorded-calls law differs by country, and an LLM summary is itself derived personal data)?

Answer: Transcripts off by default, opt-in per agent, admin-read only, 30-day retention (see Q3 answer).

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

Key: 56930f270aca
Question: OPEN: Dynamic client registration on kw. Client ID metadata documents are the preferred registration and DCR is deprecated, but some MCP clients still only register dynamically. Should `HELLO_OAUTH_DCR` be on for kw? Proposed: on, so every client works today, with the rate limit, the "unverified" consent label and the cleanup rules of S-10.

Answer: On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)

## [decision] decisions.md

Key: 5a10fd5e72c9
Question: Merge Phase 4 (PR #5) and roll out to kw

- Mark PR #5 ready, squash-merge, push images via publish, pin digests, add MinIO+SMTP+feature env to deploy/kuvryn-sync/kw, let Sync roll it out
- Hold PR #5 for review; kw stays on Phase 3 until reviewed

Answer: Merge + roll out to kw (done 2026-10-04: PR #5 squash-merged, images published, rollout commits 9832a84/492055b/290e473)

## [decision] decisions.md

Key: 5dc2499014b1
Question: Phase 7 in-call HA scope

- In-call HA for anchored calls only; NAT'd direct-media calls stay best-effort (documented limitation)
- Always-on anchoring for every call so all calls get in-call HA (reverses the Phase 5 conditional decision)
- Defer Phase 7; stop at Phase 6 scope

Answer: Anchor everything + full HA (all calls anchor, LAN-to-LAN included; reverses Phase 5 conditional anchoring deliberately)

## [spec] voice-agents

Key: 61c77b143f82
Question: 8. **Test call.** The spec offers a test extension and CDR following, not a browser or console-originated call. Do you want Hello to originate a call to a chosen phone and connect it to the agent (needs click-to-call, which Hello does not have), or a WebRTC softphone in the console (a new media stack)?

Answer: A dialable test extension per agent plus CDR following; no WebRTC and no click-to-call.

## (no longer asked)

Key: 61fa8fc32239
Question: OPEN: Migration library — goose, or golang-migrate?

Answer: goose

## [spec] voice-agents

Key: 67129a37407e
Question: 5. **TLS/SRTP between Hello and talking-agent.** Both sit on the same LAN on kw. Is plaintext SIP/RTP acceptable on kw for phase 3 with the HMAC signature (S-16), or is SIPS/SRTP a requirement before first use?

Answer: Spoken confirmation PLUS caller verification (known caller ID / allowlist, or a PIN) before any data-changing tool; tools allowlisted per agent.

## [decision] decisions.md

Key: 6844d41d1251
Question: Phase 5 media anchoring policy

- Conditional: anchor only when a feature needs it (NAT-detected, recording, announcements); direct RTP otherwise (spec §4/§16)
- Always anchor: all calls traverse the media anchor

Answer: Conditional: anchor only when a feature needs it (NAT-detected, recording, announcements); direct RTP otherwise

## [decision] decisions.md

Key: 6fee12142fca
Question: Phase 4 voicemail delivery

- Web + phone (MWI) only; email notification later
- Include SMTP email notification with audio attachment now

Answer: Include SMTP email notification with audio attachment now

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

Key: 803693c6f34d
Question: AI phase 1: auditing MCP read calls

A user's reads write no audit row today; the decision says every MCP call is audited like a user call (spec ai-external-access, S-15).

- Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)
- Every MCP tool call, reads included, writes an audit row

Answer: Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)

## (no longer asked)

Key: 8a983b0caee7
Question: Merge Phase 5 (PR #6) and roll out to kw

Answer: Merge + roll out to kw

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

Key: 9f3be89ce05e
Question: Phase 5 recording

- On-demand: DTMF (*1) and per-extension API toggle; recordings to MinIO
- Auto-record all calls (compliance style), stored to MinIO

Answer: On-demand: DTMF (*1) and per-extension API toggle; recordings to MinIO

## [decision] decisions.md

Key: a29492463871
Question: Phase 3 production SIP load balancer

- Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/
- A Hello-built Go balancer (hello-lb) shipped as a production component
- Envoy UDP proxy (L4) with active health checks

Answer: Kamailio dispatcher as the SIP-aware balancer, shipped and configured in deploy/ (the user requires a production-grade balancer, not lab tooling)

## [spec] voice-agents

Key: a9850601d3c6
Question: 4. **Caller identity and tool authority.** Today a persona runs with the tools' credentials regardless of who calls (caller ID is never identity). Do you want any caller verification (spoken PIN, known-caller list per agent) before mutating tools, or is "confirm aloud" the whole control?

Answer: talking-agent gets the same licence as Hello (Apache License 2.0, see Hello's LICENSE).

## [decision] decisions.md

Key: add7750575a3
Question: In-call HA crash detection time

- Faster detection: membership heartbeat 1 s / TTL 4 s, so a crashed node's calls re-home in about 5 s; update docs to the measured numbers
- Keep 15 s detection; correct docs to the honest numbers (crash ~15–18 s gap, restart ~4 s, graceful <1 s)

Answer: Faster detection: membership heartbeat 1 s / TTL 4 s (re-home in ~5 s); docs updated to measured numbers

## [decision] decisions.md

Key: b0654a149c2f
Question: Phone auto-provisioning: provisioning host certificate

- cluster-ca (private), with Hello's CA pushed over plain HTTP via the DHCP boot path or uploaded by hand (default the spec builds; redirect/manual phones fail until the CA is installed)
- Publicly trusted certificate (e.g. Let's Encrypt DNS-01) for a public DNS name the user owns, resolved to the kw ingress on the LAN (works on every vendor out of the box)
- Both: public certificate for phones, cluster-ca kept for the lab

Answer: cluster-ca (kw private CA), with Hello's CA pushed over plain HTTP via the DHCP boot path or uploaded by hand

## (no longer asked)

Key: b4b673d5b75c
Question: OPEN: User roles. Hello has one kind of user, who can do everything, so "the user's role bounds the scopes on the consent screen" bounds nothing today. Should phase 1 add roles (for example administrator, operator, read-only, applied to the console and the API too), or keep one role so every user can grant every scope including `secrets`? Proposed: keep one role in phase 1 (`GrantableScopes` is the single place a later roles spec plugs in).

Answer: Add roles now: viewer (read-only), operator (day-to-day configuration writes), admin (users, roles, tokens, secrets, OAuth clients, settings); existing users become admin; the console shows and edits a user's role; the API enforces a minimum role on every route; consent, MCP scopes and service accounts are bounded by role

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

Key: ccf3f94c9462
Question: Merge Phase 7 (PR #8) and roll out to kw

- Merge PR #8, wire the live-view ha flag, publish images, pin digests, Sync rollout, then kill a node mid-call on kw to prove takeover live
- Hold PR #8 for review; kw stays on current behavior

Answer: Merge + prove on kw (answered by the user 2026-10-05; PR #8 merged as 1c3fe03)

## [decision] decisions.md

Key: d774f897f39a
Question: Phase 3 PostgreSQL failover

- Test PostgreSQL outage and restart (SIP keeps running from its snapshot); real PG HA left to Phase 6 deployment guidance
- Primary/replica with promotion in the lab now

Answer: Test PostgreSQL outage and restart (SIP keeps running from its snapshot); real PG HA left to Phase 6 deployment guidance

## [decision] decisions.md

Key: d8bbc86e5884
Question: Phase 4 ring/hunt group strategies

- All five now (ring-all, sequential, round-robin, longest-idle, weighted)
- Ring-all + sequential first, the rest later

Answer: All five now (ring-all, sequential, round-robin, longest-idle, weighted)

## [decision] decisions.md

Key: dbf604d8bdf1
Question: Merge PR #4

- Squash-merge after CI/Copilot are clean and findings fixed, then start Phase 4 (PBX features)
- Hold for your own review

Answer: Squash-merge after CI/Copilot are clean and findings fixed, then start Phase 4 (pre-authorized; no further merge question)

## [spec] voice-agents

Key: e7214583272b
Question: 1. **Who is the audience of "tenant"?** Is talking-agent shared by several Hello instances (lab, kw, perhaps a customer), or only by Hello on kw plus its own web UI? This spec supports many Hello tenants with a per-tenant secret and service account; if there is only one, S-16's key ids and the tenant header can collapse to one secret and one account. (Assumed: many, because the decision calls it multi-tenant.)

Answer: One shared talking-agent for several Hello instances, tenant-scoped (per-tenant secret and service account). Transcripts off by default, opt-in per agent, 30-day retention.

## [decision] decisions.md

Key: e776054b2093
Question: Phase 1 registration store

- Valkey from Phase 1 (cluster-wide, HA-ready)
- In-memory per node until Phase 3

Answer: Valkey from Phase 1 (cluster-wide, HA-ready)

## [decision] decisions.md

Key: e8298c768bbb
Question: Deployment target and order

- Pull Phase 6 (Helm/Kubernetes on kw) forward, right after the Phase 3 merge; features (Phases 4-5) come after
- Keep roadmap order: Phases 4-5 features next; kw deployment stays Phase 6

Answer: Pull Phase 6 (Helm/Kubernetes on kw) forward, right after the Phase 3 merge; features (Phases 4-5) come after

## [decision] decisions.md

Key: e8a416e0a619
Question: Valkey failover test in CI

- Pay for a larger runner (ubuntu-4-cores) for the images job; keep the full failover test gating CI
- Keep the free 2-core runner; the failover test runs pre-merge locally and in CI only as a non-gating scheduled job
- Keep the free runner; the failover test keeps trying to pass in CI with further environment tuning

Answer: Use the kw Arc runners (the user directs CI there; labels self-hosted/linux/x64 via scaleSetLabels)

## [decision] decisions.md

Key: f9afd6b87078
Question: Phase 1 cross-node delivery to NAT'd phones

- Route via the registering node (Path + flow token, RFC 3327/5626 style)
- Test only without NAT (test phones inside the compose network); defer to Phase 3

Answer: Route via the registering node (Path + flow token, RFC 3327/5626 style)
