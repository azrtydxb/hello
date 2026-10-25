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

## Phase 5 media anchoring policy

- Conditional: anchor only when a feature needs it (NAT-detected, recording, announcements); direct RTP otherwise (spec §4/§16)
- Always anchor: all calls traverse the media anchor

## Phase 5 recording

- On-demand: DTMF (*1) and per-extension API toggle; recordings to MinIO
- Auto-record all calls (compliance style), stored to MinIO

## Phase 5 announcements

- In scope: named announcement sets played on demand (failure destinations, before transfer)
- Out of scope for Phase 5

## Phase 7 in-call HA scope

- In-call HA for anchored calls only; NAT'd direct-media calls stay best-effort (documented limitation)
- Always-on anchoring for every call so all calls get in-call HA (reverses the Phase 5 conditional decision)
- Defer Phase 7; stop at Phase 6 scope

## Phase 7 in-call HA scope (after the anchored-vs-direct explanation)

- In-call HA for anchored calls only; NAT'd direct-media calls stay best-effort
- Always-on anchoring: every call anchored so all calls get in-call HA
- In-call HA for anchored calls + policy change: LAN-to-LAN calls also anchor (cheap on a LAN, makes every call survivable)
- Defer Phase 7; stop at the current scope

## Merge Phase 7 (PR #8) and roll out to kw

- Merge PR #8, wire the live-view ha flag, publish images, pin digests, Sync rollout, then kill a node mid-call on kw to prove takeover live
- Hold PR #8 for review; kw stays on current behavior

## In-call HA crash detection time

- Faster detection: membership heartbeat 1 s / TTL 4 s, so a crashed node's calls re-home in about 5 s; update docs to the measured numbers
- Keep 15 s detection; correct docs to the honest numbers (crash ~15–18 s gap, restart ~4 s, graceful <1 s)

## Remaining items after Phase 7 (2026-10-06)

- Phone auto-provisioning vendors: Yealink, Poly, Grandstream, Snom/Fanvil, and any brand (generic per-model templates)
- Provisioning discovery: DHCP option 66, vendor cloud redirect, and manual URL entry — all three
- Provisioning auth: MAC + per-device token URL over HTTPS
- Load tests (§27): later, when the user gives a window
- SMTP relay on kw: leave voicemail email off
- Real carrier trunk check: not yet, stays pending
- Emblem: the user dropped the full design export in the repo root; use its assets, then delete the folder

## Phone auto-provisioning: provisioning host certificate

- cluster-ca (private), with Hello's CA pushed over plain HTTP via the DHCP boot path or uploaded by hand (default the spec builds; redirect/manual phones fail until the CA is installed)
- Publicly trusted certificate (e.g. Let's Encrypt DNS-01) for a public DNS name the user owns, resolved to the kw ingress on the LAN (works on every vendor out of the box)
- Both: public certificate for phones, cluster-ca kept for the lab

## Phone auto-provisioning: token hand-off for DHCP-discovered phones

- Trust on first use: an allowlisted, never-fetched MAC gets its token URL once via the boot path (optionally only from configured CIDRs), then the boot path closes for that MAC until re-armed; a second claim is flagged
- DHCP bootstraps only (CA, re-check, "not provisioned"); credentials only via redirect service or typed URL
- MAC-only on the boot path from trusted CIDRs, no token (weakest; not recommended)

## Phone auto-provisioning: vendor redirect accounts

- Automate Snom SRAPS and Yealink RPS (the user supplies API credentials); Grandstream GDMS add-device with serials; Poly and Fanvil stay a documented manual step
- Also use the by-request Poly ZTP XML API and Fanvil's undocumented FDPS XML-RPC
- No redirect accounts for now; DHCP and manual entry only, clients built and tested against fakes

## Phone auto-provisioning: phone admin (web UI) password

- Random per phone, sealed, revealable to administrators (audited)
- One site-wide password the user sets
- Leave the phones' admin password untouched

## Provisioning contract 4: MarkFetched and PromoteToken race a rotation or re-arm

Found in the Task 3 pre-PR review (prov-control). Both update by phone id
only, so a fetch that passed PhoneByToken just before a re-arm can set
boot_armed = FALSE after it (undoing the re-arm), and a PromoteToken that
lands after a concurrent rotate-token clears the token the phone is still
using. Fixing it changes the fixed prov.Store contract (Task 1).

- Pass the matched token hash to MarkFetched and PromoteToken and condition the updates on it (contract change; Task 2 and Task 3 adapt)
- Accept the race as documented (an administrator re-arms or rotates again)

## AI integration (2026-10-07)

- Order: phase 1 (OpenAPI, MCP server, agent skills), then phase 2 (in-product AI agent), then phase 3 (voice agents)
- External agent auth: full OAuth 2.1 now; Hello is the authorization server for MCP clients, with protected resource metadata, PKCE and client ID metadata documents
- MCP writes: governed by token scope; write tools only for write-scoped clients, and every call is audited like a user call
- MCP protocol: spec 2026-07-28 (falling back to 2025-11-25), official Go SDK github.com/modelcontextprotocol/go-sdk v1.8.x, stateless Streamable HTTP
- In-product agent: suggest-only like Nexora; every change is a proposal with a before/after diff; an operator applies it, replayed through the API with their permissions
- LLM: configurable endpoints (OpenAI-compatible or Anthropic), private by default; kw uses the local fastllm for now
- All AI and LLM connections use our go-ai-sdk (github.com/azrtydxb/go-ai-sdk), never another client library
- talking-agent stays a separate service and repo; Hello routes calls to it over SIP; it is changed there (personas from Hello, MCP client, auth)
- TTS: keep Breeze TTS 2 for the lab (research-only weights); make TTS pluggable for a commercial model later

## AI phase 1: user roles for OAuth consent

Hello has one kind of user, who can do everything, so the role that should bound the scopes a user can grant bounds nothing today (spec ai-external-access, S-5).

- Keep one role in phase 1: every user may grant every scope, `secrets` included; `GrantableScopes` is where a later roles spec plugs in (proposed)
- Add roles now (for example administrator, operator, read-only), applied to the console and the API as well as to consent

## AI phase 1: auditing MCP read calls

A user's reads write no audit row today; the decision says every MCP call is audited like a user call (spec ai-external-access, S-15).

- Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)
- Every MCP tool call, reads included, writes an audit row

## AI phase 1: dynamic client registration on kw

Client ID metadata documents are preferred and DCR is deprecated, but some MCP clients still only register dynamically (spec ai-external-access, S-10).

- On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)
- Off: only client ID metadata documents and service accounts

## Pivot: voice agents out of Hello (2026-10-09)

- The call/voice agents leave Hello and become a separate product; Hello talks to it only via SIP trunks.
- The built-in AI agent (phase 2: assistant, proposals, detectors) STAYS in Hello, as does phase 1 external access (MCP/OAuth/skills) and per-call quality in CDRs.
- The voice-agent feature is removed from Hello (registry, runtime API, call leg, voice_agent routing/console, voice tables). PR #65 (voice e2e) is closed unmerged.
- New requirement instead: SIP trunks must be able to reach internal numbers (inbound trunk calls targeting internal extensions), not only the outside world.

## Re-verify the fastllm-backed checks after the trunk rollout? (2026-10-14)

The phase-2 built-in agent is live, but the assistant-reply and tool-calling checks never completed because fastllm (vLLM) was down when work paused; the check cron was cancelled.

- Yes: re-run one assistant reply and one tool call through the deployed stack once the trunk rollout lands.
- No: leave the checks unverified until the next fastllm change.

  Decision: yes — verify right after the trunk rollout (2026-10-14).

## How to stop the assistant's reasoning overruns (2026-10-14)

The go-ai-sdk v0.6.0 → v0.8.0 commits do not explain the slow answers. The cause is the reasoning model sometimes thinking past Hello's 8192 `max_tokens`: the call ends on length and Hello retries it at double the budget, so the user waits for both attempts (seen: 1m17s, 2m19s; normal 2–17s).

- Limit the model's thinking for the assistant (reasoning effort / thinking budget sent per request), keeping 8192 max_tokens.
- Raise the first-attempt budget to 16384 so an overrun rarely needs a retry (no change to thinking).
- Both.
- Leave as is.

Decision: turn thinking off for Hello's model calls (2026-10-14).

## Thinking off makes the assistant skip its tools (2026-10-14)

Live on kw with thinking off: answers start in ~1s and finish in ~6s (0 reasoning tokens), but the model often says "I will check the registrations" and never calls the tool, so answers miss live data. Likely cause: in prompt structured-output mode the "answer as JSON" instruction is present on every step, and without thinking the model answers instead of calling tools.

- Split the answer: let the model call tools with no JSON instruction, and ask for the structured answer only in a final step. Keeps thinking off.
- Turn thinking back on: reliable tool use, occasional 1–2 min answers.
- Turn thinking back on for the assistant only; keep it off for detector explanations (no tools there).

Decision: split tool calling from the structured answer, thinking stays off (2026-10-14).

## Phase 6 scope (decided by the lead overnight, 2026-10-15, for review)

Phase 6 was never built; the user was asleep and asked for everything to be finished, so these judgement calls were made without asking. Each is reversible and listed so the review can overturn it.

- **kw is unchanged.** kw keeps running from Kuvryn Sync raw manifests; the Helm chart is the install path for other clusters. The chart is tested against the kw manifests (environment, probes, ports) so the two cannot drift silently. Nothing under `deploy/kuvryn-sync/` changed.
- **hello-sip is a StatefulSet, not N named Deployments.** kw's two Deployments plus the preStop stagger exist to get one-node-at-a-time rollouts and a stable node name. A StatefulSet with `OrderedReady` and `RollingUpdate` gives both natively (pod name is the node identity, and the next pod is not replaced until the last is ready for `minReadySeconds`), and scales to any N. The cost is a different shape from kw; `docs/kubernetes.md` has the comparison table.
- **Node identity is the pod DNS name** under a headless Service (`hello-sip-0.hello-sip.<ns>.svc.<domain>`), used for both HELLO_SIP_ADVERTISED_ADDR and the generated Kamailio dispatcher list, so Kamailio's by-name node check (`route[HELLO_NODE_NAME]`) matches. kw uses the short Service name for the advertised address and the FQDN in the dispatcher; the chart uses the FQDN for both.
- **hostNetwork is the default** for hello-sip and Kamailio (as kw), with NodePort/LoadBalancer as the documented option for Kamailio. RTP cannot be served by a Service (port range), so media needs a routable address; `sip.mediaAnchorHost` is `hostIP`, `podIP` or a literal. A literal address is one value for every node, so NAT with per-node public addresses is a stated limitation rather than a feature.
- **Secrets by one existing Secret** (`secrets.existingSecret`, fixed keys) instead of kw's five. The chart creates no Secret and no value carries a credential; a test enforces it. Optional AI and provisioning-redirect keys are `optional: true` references.
- **Required values fail the render:** `global.sipDomain`, `kamailio.publicHost`, `secrets.existingSecret`, and a Valkey and MinIO that are bundled or external. A placeholder default for the public host would silently produce a non-working edge.
- **Image defaults:** registry `192.168.10.131:5000/azrtydxb` (where CI publishes) and tag `main`; a mutable default, with the guide and `values-ha.yaml` insisting on `sha-<commit>` pins. There is no public registry to default to yet.
- **Bundled state is single-instance, evaluation only** (PostgreSQL and MinIO with a PVC, Valkey without one: its content is rebuilt by the nodes). HA PostgreSQL (CloudNativePG or managed) and Valkey Sentinel are guidance plus external values, not something the chart operates.
- **Kamailio has no PodDisruptionBudget and one replica** (a budget on a single replica blocks node drains); Kamailio redundancy stays the documented Phase 7 limitation. A single hello-sip node gets no budget for the same reason.
- **Upgrades are two steps:** Hello's pods first with Kamailio's image tag pinned, Kamailio second. Rolling both at once would let a hello-sip handoff's re-INVITE hit a restarting Kamailio. The kind test does exactly this and holds one call through both.
- **The kind rolling-upgrade test runs in normal CI on the Arc runner** (it needs the runner's Docker daemon, like the lab job), built from the PR's own sources; no registry is involved (`kind load`). If the runners turn out unable to run kind, the job becomes `workflow_dispatch`-only and the PR says why. The Kamailio phones port is published through a kind port mapping; phones run on the runner and use the Docker network gateway as their Contact host, the lab's trick.
- **The call-survival assertion is signalling-level**, not RTP: the call must stay in the live view, remain a connected call, and be hung up cleanly from the far side with an answered CDR. Media continuity during handoffs is already proven by the Phase 7 lab tests; the kind test proves the chart's rollout ordering keeps a survivor available.
- **Kamailio on the pod network cannot keep far-side requests of established calls across its own roll** (found by the kind job): its hello-facing Record-Route is its pod address, which changes when it is replaced. The kind job therefore asserts only that the call stays live and the caller can hang up after the Kamailio roll; hostNetwork (stable node address) is the recommended mode and the guide says so.

## Dependabot PRs #92–#95 (asked 2026-10-15)

The overnight merge queue was not allowed to auto-merge bot PRs, so these four dependency bumps are still open (gomod, npm, GitHub Actions, alpine 3.22 → 3.24).

- Merge them through the queue once CI is green (recommended)
- Leave them for a manual review

## Guard CI against GitHub action-download outages (asked 2026-10-15)

On 2026-10-15 from 04:50 to 05:35 UTC, codeload returned 404 for the pinned setup-go v7.0.0 archive, and every lab job failed. The merge queue now waits and reruns on that error. A lasting fix would be outside this repo.

- Keep the queue retry only (recommended for now)
- Add an action archive cache (`ACTIONS_RUNNER_ACTION_ARCHIVE_CACHE`) to the Arc runner image

## Make the assistant's tool phase call a tool (2026-10-15)

With thinking off, the model answers about 2–3 of 10 factual PBX questions without calling any tool, so those answers have no live data behind them. go-ai-sdk v0.8.0 can set `tool_choice` per step (`PrepareStep` + `provider.ToolChoiceRequired`), and vLLM accepts `"required"`. What should happen when the question needs no data, such as "what can you do?"?

- Force a tool call on the first step only ("required", then "auto"), and add a no-op tool `no_data_needed` as the way out for questions that need no data. It costs one extra short step on those questions, and works the same in every language. If the model takes the way out by mistake, the tool's result tells it to call the data tools.
- Force a tool call on the first step only, but skip forcing when a cheap check (keywords or a classifier call) says no data is needed. This avoids the extra step, but the check depends on language and wording, and a wrong check brings the skipped tools back.
- Do not force anything: rely only on a nudge when the tool phase ends without any call. This costs a whole extra model call on every miss.

Fallbacks either way: an endpoint that rejects "required" (400/422) is asked again without it and is never asked with it again, and a tool phase that ends with no call at all is nudged once.

Decision: lead decision (2026-10-15), pending user review: force a tool call on the first step, use the `no_data_needed` no-op tool, and keep both fallbacks.

## Address books and PTT / paging / intercom (2026-10-25)

Spec: `.procoder/specs/phonebooks-ptt.md`. Accepted by the user:

- **Address books for all brands** (Yealink, Poly, Grandstream, Snom, Fanvil), delivered as each vendor's native remote phonebook / directory XML over the existing provisioning HTTPS endpoint with the phone's existing provisioning credentials, written into the phone config so the phone fetches and refreshes it periodically. No LDAP.
- **Book kinds, all of:** a shared company book (admin-managed in UI/API, CSV import/export); an automatic extensions list that is always in sync; multiple named books assignable to phone groups/sites; per-user personal contacts in a self-service UI.
- **Server-side caller-name lookup:** on inbound and internal calls Hello resolves the calling number against the books and sets the display name (SIP From / P-Asserted display name), the web UI and the CDRs.
- **PTT, all of:** (a) server paging groups: a page code or PTT key makes Hello call all group members, auto-answer one-way (vendor auto-answer headers such as Call-Info answer-after=0 and Alert-Info), across subnets, with CDRs; (b) phone multicast PTT: Hello provisions the phones' own multicast paging/PTT channels (Poly PTT/paging, Yealink/Grandstream/Snom/Fanvil multicast listen/send), LAN only; (c) 1-to-1 intercom: two-way auto-answer by prefix code or DSS key.

Decision: accepted by the user (2026-10-25), all of the above.

## Phonebooks and PTT spec: merge and start implementation? (2026-10-25)

- Merge PR #114 and start phase 1 (recommended)
- Review the spec first, then decide
- Keep everything paused

Decision: accepted "Merge PR #114 and start phase 1".

## Phone feature parity, distinctive ringing and doorbell (2026-10-25)

Spec: `.procoder/specs/phone-feature-parity.md`. The user asked to implement ALL missing WP822/WP8x6 features plus distinctive ringing and doorbell integration: every row of both Grandstream gap analyses marked missing, partial or unverified, at every priority, including the WP8x6-only features (3 accounts, PTT channels and group paging, park codes, E911/HELD, alarm calls, check-sync reboot control, DTLS-SRTP/ICE, account sharing, roaming mode, side key, IM account, the 1000-entry phonebook cap, firmware pinning to 1.0.3.x), with a traceability table mapping every gap row to a spec item and a phase. LDAP stays out under the earlier decision of this date.

Decision: accepted by the user (2026-10-25), all of the above.

## Phase E ahead of B-D (2026-10-25)

Decision: User approved moving phase E (server paging/intercom) ahead of B–D.

## 2026-10-25: When to deploy the merged dashboard and phonebook A changes

Each deploy restarts Kamailio and both SIP nodes, which drops active calls until the hitless-upgrade fixes land.

- Wait: deploy only after the hitless-upgrade fixes (Kamailio pin, one-at-a-time SIP rollout) are merged (recommended)
- Deploy now: the dashboard fixes and phonebook A go live now, and active calls may drop
- Deploy now, but only at a time the user confirms no calls are active

Decision: accepted "Wait: deploy only after the hitless-upgrade fixes are merged".
