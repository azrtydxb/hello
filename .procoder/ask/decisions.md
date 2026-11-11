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

## 2026-10-25: ElevenLabs text-to-speech prompts

The operator writes prompt text in the GUI and generates the audio once with ElevenLabs. It is stored and replayed with no live TTS during calls.

Where the ElevenLabs API key lives:

- SOPS secret, like the AI key (recommended)
- Encrypted in the DB and set in the GUI
- Both, with the GUI value winning

Decision: accepted "Encrypted in the DB and set in the GUI". It is sealed with HELLO_SECRET_KEY.

The IVR part, since Hello has no IVR yet:

- Build an IVR/auto-attendant too (recommended)
- Prompts only for now

Decision: accepted "Build an IVR/auto-attendant too".

Update (2026-10-25): the user asked to reach ElevenLabs through OpenRouter instead of directly, and will provide an OpenRouter key later. The key is still stored encrypted in the DB and set in the GUI.

## Integrations: distribution, recordings, SSO, click-to-call (2026-10-25)

Spec: `.procoder/specs/integrations.md` (directory sync, SCIM, OIDC SSO, webhooks, event stream, click-to-call, CRM connectors).

1. Distribution: private apps per customer only. No Hello-hosted relay and no marketplace listings; the marketplace/vendor path is "not planned".
2. Recordings to CRMs: opt-in per connector, using opaque 256-bit link tokens with a 30-day TTL, revocable.
3. SSO: required for synced users. They sign in only through their IdP (OIDC for Google/Entra, LDAP bind for AD-only sites), have no local password, and directory- and SCIM-synced accounts cannot fall back to one. At least one local break-glass admin must always exist and the system refuses to remove the last one. (This differs from the recommended default of optional SSO.)
4. Click-to-call: ring-first only, auto-answer off, `user` credentials dial from their own extension, operators/admins and allow-listed service accounts may dial for others, emergency numbers refused, destinations inherit the outbound routes plus rate limits.

Decision: accepted by the user (2026-10-25).

## Flow canvas: decisions on the five open questions (2026-10-25)

Spec: `.procoder/specs/flow-canvas.md`. The user answered all five:

- **Canvases (2026-10-25):** many named, shared canvases, such as "Main office" and "After hours"; an object may appear on several, plus a computed "everything" view that is never stored. Rationale: one PBX has several call flows (opening hours, after hours) that an operator wants to look at separately.
- **AI access (2026-10-25):** full access. The in-product assistant and write-scoped MCP clients may build, change and apply canvas changes directly, for any object type including devices, phones and trunks. Rationale: the user wants the agent to do the work, not only suggest it. Safety: every AI change uses the same transactional batch endpoint, is audited with actor AI, is shown on the canvas and can be reverted; secrets are never returned to the model, which may set or generate them but reads redacted values.
- **Destinations (2026-10-25):** unify. Every "call goes to..." output accepts every call target kind (extension, ring group, queue, IVR, voicemail, announcement, paging, time condition, external number, park and so on), and a migration extends inbound routes and ring-group fallback. Rationale: one meaning for every call port keeps the canvas rules simple and removes the "numeric ring group name" workarounds.
- **Renames (2026-10-25):** the batch cascades renames to every referencing object. Rationale: references are by name today, so a rename would otherwise leave dangling targets; moving to ID references is a separate later spec.
- **Saving (2026-10-25):** always a draft plus Apply with a diff, undo/redo, live validation, one transaction and a later revert. Rationale: a call flow changes live calls, so no silent auto-save.

Decision: accepted by the user (2026-10-25), all of the above. Migration numbers 00017-00023 belong to other in-flight branches (tts, ivr, codecs, time-conditions, moh, park-pickup, queues); the flow canvas uses 00024 and up.

## Server conference rooms (2026-10-25)

`hello-pbx-spec.md` §3 listed "large-scale conferencing" as a non-goal, and the phone-feature-parity spec (S-34) kept server rooms out of scope in its earlier table rows.

Question: should Hello now build server conference rooms (audio mixing on the PBX, with a number, PINs and a live control UI), reversing that non-goal?

Decision: the user answered "yes" (2026-10-25). Conference rooms are in scope and the §3 non-goal is amended accordingly. Rooms are audio only, mixed on one SIP node per room (ownership in Valkey, see `docs/conference.md`).

In the same answer the user also said they want these features, which other agents specify separately and which this entry does not scope or change:

- SMS, WhatsApp and live chat
- video
- browser calling (WebRTC)
- mobile apps
- Microsoft Teams integration

The §3 non-goal list still names WebRTC and the rest until those specs land; only the conferencing non-goal changed here.

Video follows rooms: the user wants conference video, and video recording later (clients spec). The room keeps its state per participant and its media behind a codec interface so a video track can be added beside the audio.

## 2026-10-25: Clients and video (browser softphone, mobile apps, video, Teams)

The user decided to add four things: a WebRTC softphone in the web UI, iOS and Android apps with push for incoming calls, video calls, and Microsoft Teams integration. This reverses the WebRTC non-goal in `hello-pbx-spec.md` §3. Spec: `.procoder/specs/clients-webrtc-mobile-video.md` (draft; its five open questions are for the user). Hard ordering from the user: TLS and SRTP (`phone-feature-parity.md` S-13 to S-16) come before any WebRTC phase.

## 2026-10-25: Clients and video, answers to the open questions

Spec: `.procoder/specs/clients-webrtc-mobile-video.md`, now approved. The user answered questions 1, 2, 4 and 5; question 3 was not put to the user and the recommendation was taken.

1. **Reach: LAN and VPN first.** Internet reach is decided later, once the clients work; Phase 12 stays as an optional later step. Rationale: LAN and VPN cover the lab and most of the value, and Internet reach needs networking decisions (public name and certificate, router forwards, exposed TURN, a hardening review) that are better taken against working clients.
2. **Mobile: web softphone only for now.** The native apps, the push gateway and mobile video (sections F and G, Phases 7, 8 and 9) are not now, by user decision, until the user asks. The design stays in the spec, and nothing built before then may make them harder to add (`Platform` interface, `devices.kind = mobile`). Rationale: the browser softphone gives calling on any desk without paid developer accounts, push credentials or store builds.
3. **Media path: Go-native Pion modules in the anchor, not rtpengine** (recommendation taken, not asked). Rationale: one media path, one HA model (the in-call takeover keeps working), `CGO_ENABLED=0` kept.
4. **Teams: documentation and the SBC-peer trunk profile only.** Nothing is built or bought until a tenant and a real need exist; no certification attempt. Rationale: Direct Routing needs a certified SBC or a DRaaS provider and a Teams Phone tenant, none of which exist.
5. **Video: web video relayed without transcoding (H.264 Constrained Baseline, VP8), and video is recorded.** The recording design is S-42 and the new Phase 6b:
   - **Containers:** fragmented MP4 for H.264 and WebM for VP8, depacketized without decoding. Audio stays the existing WAV, because muxing G.711/G.722 into MP4/WebM would need transcoding.
   - **Object store:** fragments are streamed to MinIO every 10 s or 4 MiB with a manifest, and a hello-control finalizer joins them with a seek index. Video cannot be held in memory like the 10 MB audio buffer.
   - **Retention and quota:** a separate video retention (default 30 days) and a quota (`HELLO_RECORDING_VIDEO_QUOTA_GB`, alert at 80 percent, no new video recordings at 100 percent while audio continues).
   - **Playback:** a UI player that slaves the video to the WAV clock using the manifest offsets.
   - **Size:** up to about 1.35 GB per hour for a two-way call at 1500 kbit/s, against about 58 MB for an hour of 8 kHz audio.
   - **HA:** the recording state is in the replicated dialog record; on takeover new parts start at the next keyframe; a crash loses at most one unflushed fragment per stream plus the gap, and a drain flushes first.

   Desk-phone and door video waits for hardware; conference video comes after conference rooms. Rationale: the user wants video calls kept like audio calls. Streaming segments is the only way to bound memory, and a separate retention and quota keep video from crowding out audio recordings.

Not done here (outside this change, left for the user to request): the Phase 0 amendments to `hello-pbx-spec.md` §3/§5 and the `phone-feature-parity.md` dependency limit.

Applied 2026-10-25: the Phase 0 amendments to `hello-pbx-spec.md` (§3 non-goals and the "WSS/WebRTC later" principle, which is item 1 of §4) and the `phone-feature-parity.md` dependency limit.

Decision: accepted by the user (2026-10-25).

## 2026-10-25: Messaging channels, answers to the open questions

Spec: `.procoder/specs/messaging-channels.md`, now approved. The user answered questions 1, 2, 3 and 5; question 4 was not put to the user and the recommendation was taken.

1. **Providers: Twilio first, then Telnyx; Bandwidth and Vonage later.** Before go-live the customer starts 10DLC (and, if wanted, toll-free) registration in their own provider account. Rationale: both have documented, testable APIs; registration approval takes time and is the customer's account, so Hello only reports its state.
2. **Public access: not now; build LAN-only.** The separate public listener (`/hooks`, `/chat`, `/m`) is out of scope until the user decides. Consequence, stated in the spec:
   - **No inbound SMS/MMS by webhook:** a provider number cannot receive texts.
   - **No status callbacks:** status is read by polling the provider's message resource instead.
   - **No WhatsApp:** the Cloud API is webhook-only.
   - **No public chat widget:** a LAN chat widget for intranet pages is built instead.
   - **No outbound MMS:** providers fetch media from a public link.

   The phases are reordered so LAN-testable work comes first: inbox core, outbound sending on Twilio, SIP MESSAGE desk-phone texting plus the SIP trunk channel (the one LAN inbound path), the LAN chat widget, then routing, Telnyx, AI and integrations.

   Polling was checked in the provider docs: Twilio's message list (`GET .../Messages.json`, filters `To`/`From`/`DateSent`, inbound records with body) is **[verified]**. Telnyx lists inbound messages through `GET /v2/detail_records` (`record_type=messaging`, `direction=inbound`) without text **[verified]**; each body needs `GET /v2/messages/{id}`, which only works within 10 days, and that it returns inbound bodies is **[unverified]**. WhatsApp has no list endpoint that was found **[unverified]**. Polling is noted as N3, not scheduled. Rationale: the UI is LAN-only on kw today and no Internet-facing surface should exist until the user chooses one.

3. **Outbound first contact: off by default.** When an admin turns it on, each message needs an attested consent and is rate limited. Never bulk. Rationale: carrier rules and consent law, and toll-fraud exposure.
4. **Storage: plaintext in PostgreSQL with full-text search** (recommendation taken, not asked). It relies on volume encryption, has audited supervisor views and keeps messages 365 days (SMS) and 90 days (chat, media). Rationale: sealing bodies would remove inbox search; credentials stay sealed regardless.
5. **AI: suggest-only triage and drafts first.** Auto-reply is off by default and enabled per number. It answers only from admin FAQ snippets, sends at most 3 replies per conversation, discloses itself and goes through the private-endpoint guard. Rationale: customer-visible AI output needs grounding, a cap and a human handoff before it can be trusted.

Decision: accepted by the user (2026-10-25).

## Phone onboarding queue (2026-10-25)

Spec: `.procoder/specs/phone-onboarding.md`. The user wants to join a brand-new phone to the network (DHCP option 66 at `http://prov.hello.kw.watteel.lab/p/boot/`) and have it show up in a queue that phones are created from, instead of knowing its MAC first.

Decision (the user's brief, 2026-10-25): an unknown MAC's config request keeps its empty 404 and is upserted into a PostgreSQL table `onboarding_candidates` (migration `00028_onboarding.sql`, with `onboarding_ignored`). Nothing is served to an unadopted phone. The brief fixed the capture, the kinds of limits, the API, the UI and the metric; these choices were made while building it and were not asked separately:

- **PostgreSQL only, no in-memory state**, so two control pods agree: a seen phone is refreshed by one `UPDATE`; a new MAC is inserted under an advisory lock that makes the 500-row cap exact across pods. Timestamps are the database's.
- **Abuse limits:** a per-source-IP limit of 30 candidate writes a minute through the existing provisioning limiter (Valkey, memory fallback, never fail open); the 500-row cap (new MACs ignored and counted in `hello_onboarding_captures_total{outcome="full"}`, existing rows still refresh); 30-day expiry (listed and counted as gone at once, deleted by a per-minute idempotent prune on every pod); only MACs a device can have (12 lowercase hex, not all-zero, broadcast or group); a MAC that is already a phone is never written.
- **Capture points:** the per-MAC boot file of any vendor (the DHCP option 66 flow), and a common boot file when the User-Agent carries the MAC (Yealink, Grandstream, Fanvil). The vendor, model and firmware come from the User-Agent, falling back to the file name's vendor.
- **A queued phone is not a denial.** The unknown-MAC 404 used to count towards blocking the source IP (10 in 10 minutes). A phone waiting for adoption retries by design and shares an address with adopted phones, so a captured request answers the same empty 404 without that count. Rate-limited, malformed or unconfigured requests are still denials.
- **Adopt is manual phone creation:** `AdoptCandidates` calls the same `createPhone` as `POST /phones` in one transaction (device on the extension, sealed secrets, boot arming, redirect registration), so behaviour and validation match; it deletes the candidate and audits `adopt`/`adopt-batch` plus `create phone`. Bulk adopt is all or none. A phone created by hand or by CSV import also leaves the queue. The extension can be named by id, number or user (the user's linked extension). The adopt answer carries no provisioning URL: the armed phone gets it through the boot hand-off.
- **Roles:** operator and admin (the write scope, which is the operator-level scope), for the reads too; viewers see neither the page nor the queue. Dismiss can add the MAC to an ignore list, which is listed and reversible.

The user can change any of them; none is hard to undo.

## Drain-test flake: one fix, not two (2026-10-26)

- #179 and #180 both fixed the same OFFLINE-mid-test drain flake in `internal/api/cluster_test.go`. #180 (`pinMembers`: PEXPIRE the fake member records) merged first, so #179 keeps main's file and drops its `keepReady` re-publisher. Re-publishing READY every 500 ms could also overwrite the state a drain test is asserting on.

## IVR menu numbers join the park-slot dial space (2026-10-26)

IVR (#142) merged after park/pickup (#161), whose migration took 00032, so
the IVR migration is renumbered to 00033. An IVR menu number and a park slot
are both dialled as a plain number, so they must not collide: the dial-space
guard (`loadDialItems`) now counts IVR menu numbers among the names a park
slot may not equal, and creating or renumbering a menu runs `checkNotSlot`.
Both directions are covered by `TestIVRNumberNotParkSlot`. The IVR test
helper `countRTP` became `countPhoneRTP`, because park's tests brought their
own `countRTP` with a different signature.

## Conference room numbers join the dial space (2026-10-26)

Conference rooms (#172) merged after park/pickup (00032) and IVR (00033), so
its migration is renumbered to 00034 and its CHECK changes
(`inbound_routes.destination_kind`, `cdrs.kind`) now go through
`pg_temp.hello_check_values`, adding/removing only `conference` so the other
features' values survive in both directions. A room number is dialled like an
extension, so it joins the shared dial space: a room may not take a park slot
or an IVR menu number, and an IVR menu, a park lot, an extension (created or
renumbered) or a ring group (created or renamed) may not take a room number.
`TestConferenceNumberDialSpace` covers each direction. Main's music-on-hold
now hands out `media.Track`s, so the conference hold loop reads
`PCMAt(media.MixRate)` per track. In the B2BUA INVITE path the room-number
check runs before IVR, park-slot and Replaces handling (the guards keep these
disjoint, so the order only matters for speed); for in-dialog INFO DTMF the
conference consumers run before the IVR one for the same reason.

## Feature key sync migration renumbered (2026-10-26)

Main took 00031 (multicast catalog), 00032 (park/pickup) and 00033 (IVR)
before feature key sync (#158) merged, so its additive
`extensions.no_answer_rings` migration moves to the next free number; the
migrate rollback tests' re-apply counts and the "migration 000NN" comments
follow it.

## Voicemail voice after IVR and park (2026-10-26)

Main took 00032 (park/pickup) and 00033 (IVR) first, so the additive
voicemail-voice migration moves to the next free number. IVR added
`call.vmExt`, the box an IVR menu hands a caller to; this branch had already
folded the "which box" logic into `mailboxExt`, so `vmExt` now lives there
(leave mode only) instead of in `boxDetails` and `sendMWI`, which take the
extension explicitly. The IVR voicemail exit passes its extension to
`boxDetails`.

## Voicemail voice migration at 00036 (2026-10-26)

Conference rooms (00034) and feature key sync (00035) merged first, so the
voicemail-voice migration is 00036; the migrate rollback counts and the
"migration 00036" comments follow it.

## Stopped servers halt their conference mixers (2026-10-26)

`TestConferenceTwoNodes` failed about one run in four: a stopped test PBX
left its conference mixers running, and the leaked room kept sending RTP to
loopback ports a later relay in the same process bound, so the relay's
latch retargeted to the foreign stream. Serve now closes the mixers of the
rooms this node holds once it stops (`confHalt`). Leases and shared state
stay as a crashed node leaves them, because their lapse is what re-homes a
room. `TestConferenceHaltOnStop` fails without the call.

## SIP messages migration at 00037 (2026-10-26)

Conference rooms (00034), feature key sync (00035) and voicemail voice
(00036) merged first, so the SIP messaging migration is 00037; the migrate
rollback counts follow it. The Yealink template keeps both the text-message
key and the feature-key-sync keys, and hello-sip's deps carry both the
message queue and the voicemail dial/speech wiring.

## One dial-number check for every plain-number object (2026-10-26)

Extensions, ring groups, IVR menus, conference rooms and queues are all
dialled as a plain number, but each create/update carried its own pairwise
list: an extension or ring group never checked a queue, and an extension
and a ring group never checked each other. They now all call
`checkDialNumber` (store/dialspace.go), which looks the value up in all five
tables and then in the park slots, skipping the writer's own kind (a
duplicate within a kind stays the unique index's refusal). Renumbering and
renaming run it only when the value changes, so an old overlap does not block
unrelated edits. No migration: like the existing checks it is a store-level
validation, returned as the same field `ValidationError` the API already
documents (422 on those endpoints), so the OpenAPI spec is unchanged.
Feature and paging codes start with `*` and cannot equal a plain number;
they keep the `dialGuard` prefix rules. Time conditions have no number of
their own.

## MCP completeness: JSON bodies, skills, drift guards (branch mcp-skills-complete)

- Body-format gaps are closed by adding an `application/json` form to the
  existing operations (the pattern of `updateExtensionVoicemail`), not by new
  routes: one handler, one validation path, no route or role table change.
  - `createAnnouncement` / `replaceAnnouncement`: `{name, audioBase64}` /
    `{audioBase64}`. Cap 700 KiB of decoded audio, because an MCP request is at
    most 1 MiB (spec S-13) and base64 adds a third. Same `checkAnnouncementAudio`
    as multipart. Multipart (64 MiB) stays for the console.
  - `importPhones`: `{rows: [{mac, vendor, model, extension, label?, blf?}]}`
    is turned into the CSV records `checkImport` validates; `text/csv` stays.
  - `importPhonebook`: `{rows: [{first_name, ...}]}` is rendered as CSV and read
    by `phonebook.ParseCSV`, so every CSV rule applies; multipart stays.
- Firmware upload stays excluded: images are tens of MB, far over the 1 MiB
  MCP request, so base64 is not sane. Category `payload-too-large`, only
  `uploadFirmware` may use it (test).
- `importMyContacts` stays excluded under `private-data` (its old reason was
  the body format, which is not accepted; the real reason is that a user's own
  address book is private).
- Every `x-hello-mcp.exclude` now has a structured `category`, one of
  `apispec.ExcludeCategories` (credentials, private-data, self-service,
  human-in-the-loop, disruptive-device, paid-credits, binary-download,
  payload-too-large, session, spec-document). `apispec.Load` rejects a missing
  or unknown category; `TestMCPExclusionCategories` rejects reasons that blame a
  multipart/CSV/binary request body.
- Guards against future gaps:
  1. Route coverage: `TestRoutesMatchOpenAPI` (existing, kept) fails on a route
     without an operation and on scope/role drift.
  2. Exclusion reasons: `TestMCPExclusionCategories` + the loader check above.
  3. Skill coverage: `TestSkillsTeachEveryTool` fails when an included tool is
     named by no skill, or an API area (first path segment) has no taught tool;
     `untaught` / `untaughtAreas` are explicit, reasoned allowlists (empty).
- The skill name check now only treats a camelCase inline identifier as a tool
  name when it starts like an operationId (list, get, create...), so body fields
  such as `extensionId` can be written in code.
- Skills added: hello-ivr-and-hours, hello-call-handling, hello-phones,
  hello-flow-graph; hello-troubleshoot extended (cluster drain, AI findings,
  TTS settings, version, concurrency); hello-setup gets revertVoicemailGreeting.
- No migration needed.

## Flow canvas P2: the read-only canvas (2026-10-26)

Built per `.procoder/specs/flow-canvas.md` (P2). Choices the spec left open:

- Migration `00042_flow_canvas.sql` adds `flow_canvases` and `flow_canvas_nodes`
  only (`config_batches` belongs to P3). The `node_id` CHECK is a shape check
  (`<kind>:<id>`), not the kind list: the API validates the kind against the
  catalogue, so a new node type (P1, P7) needs no migration.
- Canvases and layout are not configuration: no `configChange`, no revision
  bump, no notify. Each write is audited in its own transaction (`flow_canvas`
  `create|update|delete|layout`). `layout_version` moves on every layout save;
  a save made against an older one is `409 layout_stale` and saves nothing.
- Layout of an object that no longer exists is dropped on `GET .../{id}` and
  pruned on every layout save, by checking each node id against the graph
  built from the live tables (devices and phones included).
- `PATCH` takes `viewport` but not `null`: the viewport is replaced, never
  cleared. The UI offers "Save view" (an explicit button) rather than saving on
  every pan, which would fill the audit trail.
- The UI filters the full graph (`scope=all`) by the canvas's positions instead
  of the spec's `scope=canvas:<id>`: the "not on this canvas" list needs the
  whole graph anyway, and one read keeps a single source of truth. "Import
  flow" does use the server's `scope=closure:<id>`. No change to
  `getFlowGraph`, so P1's work on it does not conflict.
- Edges to an off-canvas node are drawn as one stub per edge end, up to 150;
  past that the page says how many are left out. Clicking a stub adds the node
  (operators) or opens its panel (viewers).
- One generic node component draws every kind from the catalogue. A port an
  edge uses that the catalogue does not list (an IVR key) is added, typed like
  the node's first port of that direction. Handle ids are `in:<port>` and
  `out:<port>` because an input and an output may share a name.
- Port colours are `--flow-port-<type>`, mapped to Azrty tokens where one
  exists (call, voicemail, member, prompt, device) and to the spec's hex
  elsewhere (carrier, schedule, hold music), lifted on the dark theme.
- Dagre runs in a Web Worker from 150 nodes and cancels through an
  `AbortSignal`; below that, and where `Worker` does not exist (tests), it runs
  on the calling thread. New nodes are placed to the right of what is on the
  canvas.
- Keyboard: nodes are focusable; arrow keys move focus to the nearest node in
  that direction (stubs included); Enter or Space opens the panel (or adds a
  stub); Alt+arrow nudges a node by 20 px (operators); Escape closes the panel
  and returns focus to the node. React Flow's own arrow-key moving is off.
- The panel is view-only in P2: the object's summary fields, its connections
  (followable), its warnings and a link to the page that edits it. The nav item
  is "Call flows" at the top of the Call flow group; the canvas route is
  lazy-loaded.
- Exact dependency pins: `@xyflow/react` 12.12.0, `@dagrejs/dagre` 3.1.3.

## Flow canvas P1: unified destinations and guards (2026-10-26)

Migration 00041 widens `inbound_routes.destination_kind` with `ring_group`,
`voicemail`, `announcement` and `paging`, additively through
`pg_temp.hello_check_values`. No rows change (an `extension` route that names a
numeric ring group keeps working). Down converts `ring_group` routes back to
`extension` routes (the same call) and removes the other new kinds, as the
earlier destination migrations do.

Why only these kinds. Decision 3 asks for every kind on every output, but:

- a call that has already started (a ring group fallback, an IVR key, a queue
  exit, a time condition branch) cannot be handed to a paging group or a
  conference room: the runtime pages and joins only from the INVITE
  transaction;
- a ring group's fallback cannot ring an extension or another group. I built
  it and the test showed why it cannot: the group's ring stage has ended
  (`setup` or `setupHunt` closes `setupDone` once per call), so a second stage
  needs the forwarding machinery (`advanceStage`, which only forwards use) and
  hunt groups have no loop to return to. Making it work is a change to the
  ring state machine, not to configuration, so it is left out and written into
  S-3 of the spec;
- park is not a destination (a call is parked by a feature code or a
  transfer to a slot);
- extension forwards stay extension / numeric ring group / external number,
  because that is what `sip.forward` resolves.
  The catalogue lists exactly what is stored.

Runtime. Inbound routes of the new kinds are carried out by the destination
dispatcher time conditions already use (`applyTCDest`), so there is one place
that knows how to start a ring group, a voicemail box, an announcement or a
page. The routing table checks the target exists (`routing.Config.Named`,
loaded by both the store and the snapshot) so a route cannot dangle.

Found, not changed (a bug outside this task): a ring group whose failure kind
is `voicemail` starts the voicemail flow without setting `c.vmExt`, so the
message goes to the box named after the dialled group number, not to the
failure target. The graph draws the edge to the failure target, as the
configuration says. Worth a separate fix with a test.

Canvas model. Time conditions have no `holidays` input: a calendar attaches to
a schedule (`schedule_calendars`), so the calendar node feeds the schedule and
the schedule feeds the condition. Conference rooms stop being terminal nodes
(they are stored objects with entry and exit announcements). Park lots have no
ports. `requires` markers are gone from the catalogue; the field stays for a
feature still in a branch and is policed by `TestNoStaleRequires`.

Guards. `TestCatalogueDrift` (stored kinds, Go lists, OpenAPI enums, and the
CHECK constraints read with `pg_get_constraintdef` where a database exists),
`TestResourceCoverage` (every CRUD resource in the OpenAPI document is a node or
on a reasoned non-flow list) and `TestNoStaleRequires` fail CI when a feature
merges without canvas support. The database part cannot run in a pull request,
so the integration job is required green before merge.

The form pages (Routes) do not offer the new inbound kinds yet; they render
them through the existing fallback. The API and MCP do.

## Ring group voicemail failure box (2026-10-26)

A ring group whose failure kind is `voicemail` now sets `vmExt` to the failure
target before starting the voicemail application, the same way the IVR and time
condition paths do. Before, `mailboxExt` fell back to the dialled string (the
group number), so the message went to the wrong box or to none. Covered by
`TestRingGroupVoicemailFailureBox`, which fails without the fix.

## Routes form: new inbound destination kinds (2026-10-26)

The inbound route drawer offers `ring_group`, `voicemail`, `announcement` and
`paging` (migration 00041, API and MCP already had them). Each target is chosen
from a list, reusing `targetOptions` from the IVR destination picker: ring group
and announcement by name, voicemail by extension number (labelled "Voicemail box
of"), paging by code. A stored value the list no longer has stays selectable as
"(not found)". Choosing a kind clears the target, since a name valid for one kind
is not a target of another. An empty target is refused with a kind-specific
message. `conference` is also in the API enum and still has no chooser; not part
of this change.

## Prefer Opus over G.722 (2026-10-26) — decided: Opus first everywhere

Opus is almost never used: provisioned phones list G.722 before Opus, and
Hello-terminated media (MOH, IVR, voicemail) picks Opus only as a last resort.

- Opus first everywhere: provisioning (Opus models) and Hello's own audio; paging stays G.722
- Opus first for Hello's own audio only; provisioning order unchanged
- Keep G.722 first (no change)

## Build every unbuilt item from the 2026-10-26 audit (2026-10-26) — decided: build and track all

An audit found specced work never built. The user: "you need to build, and
track all items and partials you discovered". The tracker is
`.procoder/tracking/unbuilt.md`; every row ends in a merged PR or an explicit
hardware/external-account wait.

Competitor gaps, all chosen (multi-select, every option picked):

- Backup and restore, follow-me, blocklist, queue callback
- 2FA, dial-by-name, voicemail groups, fax
- Boss/secretary, multi-language prompts, codec in call history, phone LDAP
  directory (this reverses the earlier "No LDAP" decision for phones)

Voicemail language — decided: keep English default, add Dutch as a per-box option.

- Dutch default
- Keep English default (chosen)
- English only

Kamailio HA: option A vs B to be discussed before choosing; the user agreed
phones may be re-pointed to the new address via check-sync and `.101`
retired afterwards. VIP address chosen after the A/B discussion.

## Kamailio HA option and VIP (2026-10-26) — decided: Option A, VIP 192.168.10.109

After discussing A against B the user chose A ("A, choose any that is free"):
two hostNetwork Kamailio (master-11, master-12) behind a VRRP VIP
(keepalived or kube-vip, ARP mode). The VIP is 192.168.10.109: outside
Cilium LB-IPAM's .120-.159, not a node (.101-.108, .245-.246), no ARP reply
on 2026-10-26. 192.168.10.100 answers with master-12's MAC, so a VIP
mechanism may already run for the API server; check it before adding one.
Phones are re-pointed to the VIP via check-sync and .101 is retired after.

- A: VRRP VIP, two hostNetwork Kamailio (chosen)
- B: Cilium LB-IPAM LoadBalancer with L2 announcement

## Ring group fallback to an extension or another group (2026-10-26)

Decision 3's deviation (a ring group's fallback cannot ring an extension or
another group) is lifted: the ring state machine can open a new ring stage.

- Failure kinds `extension` (a number) and `ring_group` (a name), migration
  `00043` (additive, through `pg_temp.hello_check_values`; numbered after
  the flow canvas migration `00042` merged first). I found while
  writing it that `00041` never widened `ring_groups.failure_kind`, although
  the spec said so; the spec is corrected.
- Run time (`groupFailover`, `internal/sip/groups.go`). `openFailoverStage`
  abandons and cancels the earlier stage's legs, bumps `stageSeq`, resets
  `setupClosed` and gives the call a fresh `setupDone` (as an IVR menu does), so
  late events of the old legs never reach the new setup. The failover then
  runs the target's own ring and waits in a nested `setup` or `setupHunt`; the
  setup loop that handed over returns when it is done, so ring-all and hunt
  groups both work as origin and as target without a second state machine.
  `ringGroup` follows the existing convention that a nil `tx` means the call has
  begun.
- Extension target: its features apply from the new stage on. DND or nothing
  registered go to its voicemail box when it has one (else 603 or 480); an
  unanswered ring follows its no-answer forward or voicemail (`c.stages`
  carries the target). The box is the target's: `vmExt` is set, and
  `exhausted` sets it when the stage came from a group failover (the dialled
  string is the group number). Forward-always of the target is not followed, as
  with a forward stage.
- Group target: its strategy, members and failure destination apply; a group
  with no member to ring falls through to its own fallback at once.
- Loops: the groups rung are kept in `gfChain` and the extensions in the
  forward chain (`callStages.visited`); a repeat, a group naming itself or a
  chain longer than `groupFailMaxDepth` (= `queueMaxDepth`, 8) ends the call
  with 408 "forward loop" and the existing `loop` metric. The IVR path
  (`ivrRingGroup`) applies the same fallbacks to the menu's caller with its
  `visited` chain.
- API and store: the target must be well formed and exist, a group cannot name
  itself, and the store refuses deleting, renaming or renumbering the group or
  extension another group falls back to (`refuseIfGroupFailsTo`), like the IVR
  references. The flow catalogue offers both from the group's `noAnswer` port.
- Not done: `exhausted` after a forward stage still uses the dialled
  extension's box for voicemail (the same class of bug as the group one); it
  is reported, not changed here.

## Boss and secretary (pbx-gaps B2, S-18, S-19) (2026-10-27)

Built to the spec (D16): one assistant per boss, an assistant for many bosses,
both sets of phones ring and the first answer wins, the boss's forward always
wins over the link. The choices the spec left open:

- Table `boss_links` and column `cdrs.answered_by`, migration `00065` (the
  merge captain renumbers it). `*64` is a new feature-code action
  `boss_divert` (CHECK widened additively through `pg_temp.hello_check_values`),
  seeded for an existing install only when the code is free, and in
  `DefaultFeatureCodes` for a fresh one. The spec's S-1 also lists `*74` and
  `*75` for follow-me; they are the park codes in the defaults today, so that
  clash is for the follow-me item to settle, not this one.
- API: `GET|PUT /api/v1/boss-links`, a whole-list replace like the feature
  codes (the spec names GET and PUT only). Viewers read, operators write. An
  assistant may carry `outboundAsBoss` on one link only: with two bosses the
  caller ID to present would be a guess, so the API refuses it, and the
  snapshot ignores the flag if it ever finds two.
- Runtime data rides the snapshot (`snapshot.BossLink`, enabled links only,
  `divert_on` included). The hook is in `ringExtension`, after forward always
  and before DND, so a boss reached by a forward, an IVR option or a
  direct dial rings the same way. Not covered: a boss that is a ring group
  member, a queue agent or a transfer target rings as a plain extension.
- DND on the boss, the divert and "busy with call waiting off" silence only the
  boss's own legs; the assistant still rings. With no registered assistant
  device the link does not apply and the boss rings as usual, so a call is
  never lost for want of an assistant. The assistant's own forwarding and DND
  are not consulted: the link rings the assistant's devices.
- `assistant_first` starts the boss's legs after `bossAssistantLead` (10 s) or
  when a screened call is put through. The pending count includes the delayed
  legs, so a busy assistant does not end the call before the boss has rung;
  the cost is that the boss rings after the full 10 s even if the assistant
  declines at once.
- A call from the assistant to the boss (or the boss to itself) skips the link,
  so the assistant's other desk phone is not rung for the assistant's own call.
- Screening is a gate on the assistant's leg, before the leg can claim the call
  (`screenLeg`, in `leg.drive`): the leg is acknowledged, the relay's callee leg
  points at it, both legs are muted against each other, and the prompt plays to
  the assistant while the caller keeps hearing ringback. 1 releases the boss's
  legs and drops the assistant; 2 sends the caller to the boss's box
  (`evScreenMessage`, handled by the setup loop so it is the only goroutine
  that answers the caller); no key after two rounds of the prompt, or the
  assistant hanging up, is no choice. The assistant never speaks to the caller
  on a screened call. That is what the spec says ("1 connects to the boss"); the
  assistant who wants to talk first uses the ordinary call, with screening off.
  Keys come from RFC 2833 (the relay's DTMF hook) and from SIP INFO (the
  screened leg's dialog is matched while it is being screened, `matchDialog`).
  Screening needs the anchor and an early offer; otherwise the answer is an
  ordinary one and the trace says so. The prompt is `boss.screen` in both
  language tables, spoken through the voicemail speaker, a tone when none is
  configured. Only one device of the assistant is screened at a time; another
  that answers meanwhile is dropped.
- IVR: a menu option that dials the boss rings the assistant too, and screening
  is offered there as on a direct call. A menu's call always rings both sets of
  phones at once (the ring mode does not apply), so putting a screened call
  through has nothing to release; 2 goes to the boss's box through the menu's
  voicemail exit.
- Transfer: a call transferred to a boss rings the assistant's phones as well
  (no screening, no `answered_by`, the boss's own DND and divert are not applied
  to the transfer's legs, which is how transfers treat DND today).
- Early decline: with `assistant_first`, once every assistant leg has ended
  without taking the call (486, 603, unreachable, hang-up, no screening choice)
  the boss's legs start at once (`bossAssistantFailed`), not after the 10 s.
- Not built, and why: a boss who is a ring-group member or a queue agent rings
  as an ordinary extension. S-18 and D16 define the link for "calls to the boss";
  a call to a group or queue is a call to the group or queue, and no spec line
  asks for the link there. The spec excludes: "Two or more assistants on one boss,
  and a boss-side call-screening queue" (pbx-gaps.md, Out of scope, line 287).
- CDR: `answered_by` is the assistant's extension when the assistant answered,
  empty otherwise (not filled with the answering extension of every call). The
  trace reads "Answered by assistant 300 for 200" (extension numbers, no names).
  A screened call the assistant sent to voicemail or through has no
  `answered_by`: the assistant never took it.
- `*64`: dialled as a call from the boss's own phone, or inside a call, it flips
  `divert_on` of the boss's link (computed from the snapshot, written through
  the settings sink with the audited `ApplyFeatureUpdate`, one transaction with
  the revision bump). Two presses before the snapshot reloads send the same
  value; the phone hears the 200. From an extension that is no boss it is 404.
  Metric `hello_boss_divert_total{result}` (on, off, no_link, failed). The
  screening choices are logged with the two extension numbers only, and are in
  the call trace.
- Calling out as the boss: `routing.Call.CallerIDExtension` selects the
  extension whose external number and caller-ID rule the outbound decision
  uses; the route is still matched for the assistant's own extension, so the
  flag grants no dialling rights.
- Not done here: the boss's BLF
  does not show the divert; no second assistant per boss (the spec's out of
  scope).

## Syslog sink (S-27, item 36) (2026-10-27)

- **Where.** In hello-control (package `internal/syslogsink`), not a sidecar: it needs the phone table
  and Valkey, both already there. Off unless `HELLO_SYSLOG_UDP_ADDR` or `HELLO_SYSLOG_TLS_ADDR` is set.
  The pod binds 5514 and 6514 (non-root); Services map them to 514 and 6514.
- **Storage.** Valkey lists in `internal/livestate/syslog.go` (it owns a test database; every Valkey
  test database is taken): `hello:syslog:phone:<id>` capped 10000 with a 24 h TTL; unknown sources
  under `hello:syslog:ip:<ip>` capped 1000, indexed in a sorted set bounded to 200 addresses whose
  evicted lists are deleted in the same script.
- **Attribution.** By source IP = `phones.last_fetch_ip`. An address two phones share is treated as
  unknown rather than guessed. Syslog carries no reliable phone identity otherwise.
- **Rate cap.** Token bucket per phone (per address for unknown), default 20/s burst 200; drops are
  reported by one stored notice line instead of a counter key.
- **Redaction.** There was no shared line redactor, so `syslogsink.Redact` combines the Authorization
  header, credential key/value, provisioning-path and 52+ char token rules (same token shape as
  `prov.RedactPath`), plus URL passwords. It runs before truncation and storage.
- **Config.** Group setting in two columns on `phone_groups` (migration 00064), default off, level
  default info; `PUT /api/v1/phone-groups/{id}/syslog` (admin). Template change is Grandstream only
  (P207/P208, as the spec says [R8x6 TPL]); other vendors are not touched. Off is written explicitly
  (P207 empty, P208 0) so disabling reaches phones. P208 mapping debug 1, info 2, warning 3, error 4
  is read from the template and UNVERIFIED on hardware, as is a `host:port` in P207 (kw needs it: the
  NodePort is 30514).
- **kw manifest.** UDP only (no TLS cert on kw yet): `hello-syslog` NodePort 30514, traffic policy
  Local to keep the phone address; works only on a node with a hello-control pod. Not deployed.
- **API/UI.** `GET /api/v1/phones/{id}/syslog`, `GET /api/v1/syslog/unknown`; Syslog tab on
  PhoneDetail, toggle and level in the phone group editor. No UI for unknown sources.
- **Follow-ups (built).** Counters received/dropped/unmatched; a minimum-level filter on the tab (a
  line without a priority always shows); Phones > Unknown syslog page; Yealink, Poly and snom keys
  (all marked verify; Poly has no port so it needs UDP 514; the level scales are mapped from the guides:
  Yealink debug and info 6, warning 4, error 3; Poly 0, 1, 4, 5). Fanvil has no documented syslog keys, so
  none. Migration number: 00065 went to PR #236 (boss links), so this one is 00064.

## Messaging inbox core (2026-10-26)

Delivery item 1 of `.procoder/specs/messaging-channels.md`, built LAN-only on the
SIP text messaging of PR #165. Migration 00063 `messaging_core`; scope `messages`
(granted to the user, operator and admin roles, never to viewer).

What is in: channels (provider `sip_trunk` only) and numbers, the conversation
model, the idempotent ingest (event ledger, one open conversation per number and
customer under a partial unique index), Hello's own opt-out engine with the HMAC
residue, retention and erase, supervisor-view audit, the `EventSink` (no-op),
`/api/v1/messaging/*`, `/api/v1/inbox/*` and `/api/v1/me/inbox/*`, the SSE
stream, browser and email notification, the Inbox, Channels, Opt-outs and
Settings pages. Assignment is by hand and by claim; the only route kind is
`inbox` (the shared pool).

How it ties into #165:

- `inboundTrunkMessage` offers a text to `sip.ConversationGateway.InboundFromTrunk`
  before the extension route. The gateway (hello-sip, over the control store)
  returns handled only for an enabled number of an enabled `sip_trunk` channel on
  that trunk; every other number takes the old path unchanged. A store failure
  answers 503 so the carrier retries. The event key is trunk, Call-ID, CSeq,
  sender and dialled number.
- A reply is queued in the existing `messages` table in the same transaction as
  the conversation message (`messages.conversation_message_id`, added here), sent
  by the trunk of its channel from the number the customer texted. `FinishMessage`
  and `MaintainMessages` move the message to `sent` or `failed`; with no queue row
  left after five minutes it becomes `unknown`. A reply is never `delivered`:
  there are no receipts on this path.
- A reply is split at 700 bytes (the store's `MaxMessageBytes`, one UDP packet),
  not the spec's 1000, so no part is refused for its size.

Deviations from the spec text, on purpose:

- The live stream wakes by polling `conversation_events` once a second per open
  stream, not by Valkey pub/sub; the table is the source of truth either way and
  Valkey is not needed. Event writers take an advisory lock so ids commit in
  order and a resume by `Last-Event-ID` cannot skip one.
- An address is matched by its lookup key, or by its last nine digits when the
  keys differ, so a carrier that dials the number without its `+` still finds it.
- Not in this item (later items): media tables, canned responses, typing,
  first-contact sending, queue and user routing, the provider interface and
  Twilio, desk-phone delivery, the chat widget, AI, export, reports.

The inbox navigation stays hidden until an enabled channel has an enabled number
(`GET /api/v1/me/inbox/status`). Message text is excluded from MCP; only
`getInboxStats` is a tool.

## Integrations P3: click-to-call (2026-10-27)

Built from spec `integrations.md` I-37 to I-39. Choices the spec left open:

- **The phone's answer drives the ordinary call path.** `call` and everything
  under it (recording, anchoring, hold, transfer, voicemail, events, in-call
  HA) assume the caller is a UAS dialog (`c.dss`, 43 uses). Rather than a
  second call engine, leg A is an INVITE to the extension's contacts with no
  SDP, and the phone's 200 (carrying its offer) becomes a synthetic INVITE
  "from the extension to the destination" that enters `routeCaller` (the part
  of `handleInvite` after digest verification, split out unchanged). The
  synthetic request mirrors the phone's dialog: Call-ID, the phone's tag as the
  From tag, our From tag as the To tag (set in `originateTx.attach`, because
  sipgo draws a random To tag in `ReadInvite`), the 200's Contact, its
  Record-Route reversed, its source address and our CSeq. A transaction
  adapter (`originateTx`) stands in for the server transaction: the engine's
  answer becomes the ACK of the phone's 200 (with the SDP answer), a final
  failure becomes a BYE, the phone's BYE before the destination answers is the
  caller's CANCEL. Then BYE, re-INVITE, hold and takeover from either side
  work with no click-to-call code. The cost: three small hooks in `b2bua.go`
  (`begin`, `byeA`, `handleBye`) and one in `record`.
- **Leg A is a late-offer INVITE.** The destination's SDP answer is the first
  media description the phone sees, so no media address is promised before the
  destination answers. A phone that answers without an offer fails the request
  `agent_no_answer` (it cannot be bridged).
- **Not on 180**: leg B is dialled only on the phone's 2xx, as the spec says;
  the phone is ACKed only when the destination answers (RFC 3261 permits it,
  the 200 is retransmitted meanwhile). A phone that hangs up in between is
  handled (the destination's ring is cancelled).
- **State**: the live state is Valkey (`hello:originate:state:<id>`, an hour);
  `originate_requests` is the record. `GET` shows the live state and copies a
  changed state into the row; a sweeper in hello-control (one pod at a time,
  advisory lock) copies the rest, closes requests stuck for 60 s
  `node_unavailable` (a connected one after 12 h `ended`), frees the setup
  slot and purges finished rows after 30 days. Migration 00056 (the highest
  open PR migration is 00046; the merge captain renumbers).
- **Limits** are fixed windows (the window number is in the key, so the
  Retry-After is the time left). A request first takes the extension's setup
  slot (`SET NX`, released by the node once the request is past
  `ringing_agent`, by the sweeper or on any refusal), then counts credential
  minute and day, extension hour, destination and global. A replay with a known
  idempotency key is answered before any counting. Five refusals in ten
  minutes raise one `originate.anomaly` audit row per credential per window.
- **Emergency** is refused at the API (`422 emergency_not_allowed`) from the
  route table's `emergency` flag, and again at the node. Destinations the
  routing denies, feature codes and paging codes fail `route_denied` at the node
  before the phone rings (the control process cannot tell a ring group or
  queue number from a bad one). The administrator's country restriction
  travels with the queued request and is applied by the node to outbound
  routes only (internal numbers are never restricted); a prefix may match the
  dialled or the rewritten number.
- **Caller ID**: the presented number is always the routing's choice for the
  extension (the engine has no caller-ID input for an extension caller). A
  `callerId` in the request is accepted only if it equals the extension's
  number, its external number or what the outbound route presents for that
  destination, else `422 caller_id_not_allowed`; so it is a check, not a
  selector, until the routing offers more than one number per extension.
- **Who**: a user or viewer rings only their own linked extensions (`403
forbidden_role` for another, `403 no_extension` with none); an operator or
  administrator any; a service account any inside its `extensionScope` (any
  when it has none), and one extension in scope is the default. `GET` and
  `DELETE` belong to the credential that started the request (same audit
  actor) and to operators and administrators (not to service accounts, which
  see their own); anyone else gets 404.
- **Auto-answer** asked for while the administrator has not enabled it is a
  `400` on `autoAnswer` (the request is not downgraded to a plain ring).
- **DELETE answers 202** with the state at that moment: hanging up is
  asynchronous. A queued request no node took is closed by the DELETE itself.
- **New reason `cancelled`** (client DELETE, or the phone hung up while the
  destination rang), added to the spec's list; new error codes
  `emergency_not_allowed`, `caller_id_not_allowed`, `route_denied`.
- **MCP**: the three originate operations and the policy operations are
  excluded (`x-hello-mcp.exclude`). The spec excludes originate; the policy
  (toll-fraud controls) is excluded on the same grounds.
- **Policy routes** are `GET`/`PUT /api/v1/integrations/click-to-call` (admin),
  not under `/calls/originate`, which `{id}` would shadow. Settings live in the
  singleton `originate_settings`; the policy shown on Integrations > Overview.
- **`/dial` follows the P1 event stream** (`call.originate.progress`, filtered
  by `originateId`) and does one `GET /calls/originate/{id}` when the stream
  opens, to cover a change that came before it. Only if the stream drops or is
  refused does it poll once a second. The page is a nav item in _My phone_ and
  a route for every role. `tel:` registration is the explicit _Use Hello for
  phone links_ button.
- **`hello-dialer` is registered** as a `preregistered` OAuth client
  (`oauth.PreregisteredClients`, upserted by hello-control at start when
  `HELLO_PUBLIC_URL` is set): a public PKCE client, Verified, redirect URI
  `<public URL>/integrations/dialer/callback`. The console dialer itself uses
  the session cookie; the client is there for the pop-up consent flow later
  apps reuse.
- **Setup timeout** is `HELLO`-independent: 30 s (`Config.OriginateRing`), not
  an environment variable; the request carries no override.
- **Audit** rows: `originate` (the extension and the first four digits of the
  number, the rest masked), `originate_refused`, `originate.anomaly`,
  settings `update`. The OAuth client is the audit `via` automatically.
- **Device**: the API takes the numeric device id and the queued request
  carries its SIP username; a device of another extension is a `400`.

## 2026-10-27 Phone feature parity item 19: firmware policy (S-26)

- Already on main: `prov_firmware` and `prov_firmware_pins`, upload and pin API
  and page, `firmware_seen` from the User-Agent shown as plain text, P238 = 0.
- Minimum versions are a table (`prov_firmware_minimums`, migration 00056) with
  an admin PUT/GET, not a constant, because the spec says per vendor and model
  glob. Seeded: Grandstream `WP822*` min 1.0.11.22 (R822 RN). A rule's
  recommended version holds the "WP defaults" of the spec: WP822 1.0.11.78
  (lab build) and WP8x6 `WP8?6*` 1.0.3.39 (the newest 1.0.3.x template in the
  spec; the lab pick among 1.0.3.29 to .39 is the admin's to change). It is
  documentation only and is never rendered. WP8x6 has no floor in the spec, so
  its min is empty.
- Enforcement: a pin is refused (400, field error) when its version is below the
  highest minimum of any rule whose glob matches the pin's glob or the reverse
  (conservative, so `*` cannot smuggle a file under a floor). Rendering skips a
  pin below the minimum of the phone's actual model, so lowering nothing is
  needed when a floor is raised later.
- Drift: computed server-side in `ListPhones` and `GetPhone`
  (`firmwarePinned`, `firmwareMinimum`, `firmwareDrift`). Drift is seen != pin
  (numeric compare, so 96.86.0.070 equals 96.86.0.70) or seen below the floor.
  No version seen is not drift. Other phone readers (push, render) do not
  carry the fields.
- Upgrade window: an optional `upgrade_hour` on the pin. Grandstream WP8x6 and
  WP8xx only: P194 = 2 with P285 = hour, and P238 = 1. P194 is the phone's
  whole automatic upgrade (config too), so a window makes config resync daily
  unless check-sync is sent; the window is therefore opt-in. P285 and P194 = 2
  are the generic Grandstream keys and are not verified on a WP (hardware
  check). The WP856 (P22296) and other vendors get no window until their keys
  are known; the page offers the hour for Grandstream pins only.
- TR-069 settings not touched.

## Negotiated codec per leg in the CDR (2026-10-26)

`cdrs` gets `caller_codec` and `callee_codec` (migration 00055, nullable text,
`name/clock rate` such as `PCMU/8000`, `G722/8000`, `opus/48000`), fed by the
same `logCodec` bookkeeping as the `call codec` log line, so the two never
disagree. Choices: the value is the last codec the leg settled on (a re-INVITE
that switches codec replaces it); payload type and ptime stay in the log only;
a leg that never had a codec (unanswered call, the far side of media Hello ends
itself, rows from before the migration) is NULL, shown as a dash. The list and
detail API return `callerCodec`/`calleeCodec` (null when absent), the CSV
export appends `caller_codec` and `callee_codec`, and Call history has a Codec
column ("a" when both legs agree, "a → b" when they differ).

## Session timers on moved dialogs and trunk legs (S-9, follow-up 9b)

- Parked call: the call keeps one dialog, its party's (`link.selfSide`). Its
  timer used to re-arm for ever while the call was linked, so nothing was
  refreshed. Now only the party's dialog is refreshed, or must refresh when
  the phone took the refresher role; the released parker's timer just ends.
  A refresh from the party goes through `sessionIn` from `linkedReinvite`, so
  a phone that refreshes keeps its call, and the SDP last given to the party
  is recorded for a re-INVITE refresh.
- Retrieve and pickup: the retriever's and the picker's dialog is answered by
  `answerSelf` through the raw transaction (no sipgo dialog). It gets a timer
  only when the call is linked (a voicemail or an announcement gets none),
  Hello as refresher at its own interval, and refreshes are built from the
  INVITE and `vmTag` (`sendRawCallerRefresh`), with a CSeq past anything sent
  on the dialog (`nextCSeq`, also used by `byeCallerRaw` so its BYE follows a
  refresh). The picked-up caller's 200 carries the negotiation, and the
  dead-media watch starts for it, since the call had no answer before.
- Returned call (parker rung again): the call that rang the parker owns the
  parker's dialog and negotiates its timer from the parker's answer; the
  held party stays on the original call.
- Dead media for a linked pair: watched on the call that holds the relay, not
  while either party signalled hold (`linkHeld`, set on both calls of the
  pair), with the silence clock restarting after a retrieve.
- Blind transfer, attended transfer: the call that carries the caller on takes
  over the caller dialog's negotiated timer (copied, new timer) and negotiates
  the target's leg from its answer; the attended bridge copies each dialog's
  timer from the call it came from. The SDPs the hand-over re-INVITEs sent are
  recorded for refreshes. A transfer into a queue is not changed: queue and
  IVR calls have no session timers today (their dialog requests do not pass
  `sessionIn`), so carrying one over could end a call whose phone refreshes.
- Trunk setting: `trunks.session_timers` is `default`, `on` or `off`
  (migration 00065, TEXT with a CHECK, `default` for every existing trunk, so
  nothing changes on upgrade). `on` requests Session-Expires on legs to the
  trunk and gives a call from it Hello's interval when the carrier sent none;
  `off` does neither; `default` follows `HELLO_SIP_SESSION_TIMERS_TRUNKS`,
  false by default, so an existing install behaves as before. A global
  `HELLO_SIP_SESSION_EXPIRES_SECONDS=0` wins over every trunk. A carrier that
  sends Session-Expires itself is honoured on a trunk set to default (as in 9) and ignored on one set to off.
- A trunk refresh answered 401, 407, 403 or 488 proves the far end is alive,
  so it does not end the call (some carriers do not take an in-dialog
  re-INVITE with credentials or a re-offered SDP); no answer, 408, 481 and the
  other failures still do.
- HA takeover: the call's negotiated timers (interval, refresher, UPDATE) ride
  in the replicated dialog state (`DialogState.Session`, by dialog: caller or
  callee, whatever `Legs` holds), also for a parked call, which replicates only
  its party's dialog. A survivor resumes them as they were (a 6 s timer that
  Hello refreshes is refreshed at 3 s, a phone that must refresh is ended when
  it does not) instead of restarting at its own default. From an owner that
  sent none, a taken-over call gets Hello-refreshed defaults only on dialogs it
  still has: a one-legged call (menu, queue, voicemail) or a parked call used
  to be given a callee timer whose refresh could not be sent and ended it.
- IVR menus and queues: the caller's dialog gets a timer when the application
  answers, whoever refreshes. A caller that names itself the refresher is
  honoured: the application's dialog answers its re-INVITE or UPDATE refresh
  with 200 and the SDP Hello last gave it (direction mirrored, the relay
  re-aimed at a new media address) instead of 481, which would end its session
  (`soloInDialog`; a queued caller already had this through the anchored
  handler, the menu phase answered 481); a caller that stops refreshing is ended as on any call.
  When the application connects the caller to a destination, the call keeps
  the caller's timer, gets the destination's, and the dead-media watch starts
  (it was off for the whole call before). A transfer into a queue carries the
  caller's timer over.
- Not given session timers, with the reason: voicemail, announcements, paging
  and music on hold are short and bounded by the application; a conference
  participant is on the room's mixer and handles its own in-dialog requests; a
  voicemail and announcement calls are answered through the raw transaction and
  have no timer to refresh or answer a refresh for, so only the menu and queue
  dialogs answer refreshes.

## Door station extension kind and Door group (2026-10-27) — phone-feature-parity item 13 (S-45, S-46)

- Migration `00053_door_station`: `extensions.kind` (user/door, default user),
  `extensions.door_targets` (jsonb `{"extensions":[ids],"ringGroups":[ids]}`, ids
  not foreign keys; the snapshot drops an id that no longer exists),
  `ring_groups.is_door`, and `door` added to `cdrs_kind_check` through
  `pg_temp.hello_check_values`. The open code, hang-up flag and webhooks belong
  to items 14 and 16 and are not in this migration.
- A station may call its listed extensions, its listed ring groups and every ring
  group marked door (reading S-45 "the Door ring group" as any Door group). A
  door station can be listed as no one's target and cannot list itself.
- Enforcement is in `handleInvite` right after the caller is verified: the
  dialled number goes straight to the ring group or extension and never through
  routing, so no route can send it out; anything else is `403` with the CDR kind
  door. The trunk ban is also in `acquireSlot` (every trunk leg takes a slot), so
  a callee's forward-always, a group's external failure, an IVR dial-out and a
  transfer cannot carry a station's call onto a trunk. Emergency routes are
  never blocked (S-45 edge case). Transfer copies the flag to the new call.
- Call kind: a call from a station, or into a Door group, has kind door, so rule
  1 of S-40 gives it the doorbell tag; the caller kind for rules is `door`. Kind
  already set to page or intercom is not overwritten by reaching a Door group.
- No voicemail, no groups: creating a station makes no voicemail box; turning a
  user into a station switches voicemail and intercom off and hides it from the
  extensions phonebook (admin may show it); it is refused (field `kind`) while the
  extension is in a ring group, paging group, pickup group or queue. The same
  members are refused on write (`checkNoDoor`), the snapshot drops a station from
  ring group members, and a station has an empty pickup scope and is in no
  one's. Intercom to a station is `403`. MESSAGE to or from a station is `403`
  (SIP) and `ErrMessageForbidden` (store); broadcast skips stations. Page needs no
  extra code: a station is never a paging member.
- A new Door group with no ring tag gets `doorbell` (S-46), stored explicitly so
  the group shows it; later edits keep whatever tag is set.
- Hello still does not provision door stations; the station's button dials the
  group's number, set on the unit.
- HA: `Door` and `Kind` ride the replicated dialog state (`haContinuity`, which
  builds it under the call lock, covers answered, homed and parked calls), and
  `haNewCall` restores them, so a taken-over door call keeps the trunk ban and CDR
  kind door (`TestDoorCallerSurvivesTakeover`).

## Ring map provisioning, phone-feature-parity S-42 and S-44 (2026-10-27)

- Storage: `ring_map` (migration 00051) holds explicit choices only, keyed by
  (group or global, tag, vendor), tone being a vendor ringer id. A tag and
  vendor with no row use the vendor's default for the tag's current Bellcore
  slot, so the map needs no seeding and a new custom tag works at once. The
  tag foreign key is RESTRICT; the delete handler names the map in its 409
  (spec edge case: refused until unassigned). A group's rows go with the group.
- The map is read only by provisioning, so a change does not bump the
  configuration revision (hello-sip does not use it). It rides in
  `RenderData.Ring`, which the existing sync hash covers, so the automatic
  resync (S-5) pushes a changed map with no new trigger.
- Group over global over default is merged in the store (`ringInfo`); the
  template only sees the result (`prov.NewRingInfo`, pure and unit tested).
- Tones offered per vendor are the vendor built-ins of S-42 only: Yealink Ring
  1 to 8 and Bellcore 1 to 5, Poly ringer1 to 24, Grandstream system/1/2/3, Snom
  Ringer1 to 10, Fanvil Type 1 to 9. Silent only on Poly and Snom (the other
  vendors' lists have none we can cite). Custom tones (S-43 tone library) are
  a separate item and not offered; the Grandstream custom tones 1 to 3 are
  whatever the phone holds. Grandstream defaults are all `system`, because a
  custom tone may not exist on the phone.
- Defaults per Bellcore slot: Yealink Ring N, Fanvil Type N, Snom RingerN, Poly
  ringer(N+1) (ringer2 is Poly's own default for slot 1).
- Poly matches on the text `hello-<tag>` (as the existing auto-answer rule
  does), not the URL the spec table names: all tags of one slot share the same
  URL, so a URL match could not tell them apart. Poly rule 1 stays the
  auto-answer rule; ours start at 2 with classes custom1..custom23.
- Tag names that start another tag's (`door`, `doorbell`) would let the
  shorter text shadow the longer on a substring match; rules are written
  longest text first (stable on priority).
- Slot limits: Yealink 10, Fanvil 10, Grandstream WP8xx 10, Poly 23, Snom 3
  classes, WP8x6 5 Bellcore slots. Past a limit the first tags in priority
  order (built-ins in canonical order, then custom by name) are written. On a
  WP8x6 the first tag of a Bellcore slot decides that slot's tone; the page
  warns about shared slots.
- Snom has three named classes: only internal, external and ringgroup are
  written (the tags SIP sends `alert-*` for); other tags have no Snom rule.
- The line's own ring tone follows the internal tag (spec S-37 for the
  Grandstream P104; the same for the other vendors' "default line tone" row).
  Yealink's `ring_type` takes `RingN.wav`; Bellcore ringers have no known file
  name there, so the line key is left unset for them.
- No Grandstream WP856 or GRP/GXP keys: the spec names none ("verify key"), so
  those models get no ring map rather than a guessed key. For the WP8x6 P95026
  is written as five comma-separated tones (slot 1 to 5) and for the WP8xx the
  rules as P1488 + 2(n-1) (match text) and the next P-value (tone): the spec
  names the keys but not the field layout, so both layouts, and the Fanvil rule
  keys (`SIP1 Alert Info Text/Ring n`) and the Yealink Bellcore value (8 + n)
  are marked verify in the templates and in docs/ring-tags.md.
- API: `GET /api/v1/ring-map` (vendor lists, defaults, choices),
  `PUT /api/v1/ring-map` (replace global), `PUT /api/v1/ring-map/groups/{id}`
  (replace one group; empty list removes). Whole-list replace, like the rules.
  Operator for writes, viewer reads. Three MCP tools follow from the spec.
- UI: the page is now called Ringing (`/ringing`; the old `/ring-tags`
  route is gone, it was never linked outside the nav) with a new Ring map tab (a Tones tab follows with S-43).
- Tests that touch real configs assert on lines inline; no golden file of a
  rendered phone configuration is added.

## Tone library (phone-feature-parity S-43), 2026-10-27

- Built on ring-map (#226), which depends on #204. Migration 00052 is the next
  number after ring-map's 00051 (main has 00045).
- Storage: table `ringtones` (id, name unique, object_key, size, sha256,
  sample rate, duration) and the audio in MinIO bucket `hello-ringtones` under
  `<name>/<sha256>.wav`. The hash in the key means a name that is deleted and
  uploaded again with other audio never overwrites an object a phone may still
  be fetching. The spec says `DELETE /api/v1/ringtones/{id}`, so the row has an
  id; the name is also unique and is what the phone URL and the ring map use.
- A name is `[a-z0-9][a-z0-9-]{0,31}`, never changes and cannot be re-uploaded
  while it exists (409), so a phone's ETag for a name is stable.
- Format check is by the bytes (RIFF/WAVE, fmt tag 1, mono, 16-bit, 8000 or
  16000 Hz, at most 30 s, file at most 512 KiB, the request at most 576 KiB);
  every refusal is a `400` field error on `file` or `name`.
- Ring map reference: the API id is `custom:<name>`. In the table the choice is
  `tone = 'custom'` plus `custom_tone_id` (FK, ON DELETE RESTRICT) with a CHECK
  that the two agree, so the existing `tone` regex CHECK is not widened (it
  already accepts `custom`) and no value-list CHECK needs changing. Deleting a
  tone a choice uses is a 409 naming the choice.
- The one CHECK that is a value list, `prov_fetches_kind_check`, gets `ringtone`
  through `pg_temp.hello_check_values` (additive; the Down removes the rows of
  that kind first). The Go contract test reads 00052 for it.
- Vendors given a custom tone: Yealink (`ringtone.url` = the phone's tone URL
  under its own token, rule `.ringer` = `<name>.wav`) and Snom
  (`custom_melody_url`, class sound `Custom`), the spec's keys for the "custom
  tone delivery" row. Both keys take one file, so a phone is delivered the
  first custom choice in tag order; another custom tone on the same phone plays
  the default for its slot. The value forms are marked verify in the templates.
- No key, so no custom tone: **Poly** (the spec names `saf.x` for the file but
  no ringer value that plays it; guessing the saf-to-ringer mapping is not
  allowed), **Fanvil** (the spec says "custom ring URL key (verify)" and names
  none), **Grandstream** (format verify, not produced, as the spec says). The API
  refuses `custom:` for those vendors (400) and the page only offers library
  tones to Yealink and Snom. A Grandstream template never references a custom
  tone (tested).
- The Yealink line key `account.1.ringtone.ring_type` is left unset when the
  internal tag maps a custom tone (it takes a built-in ringer or a sound file
  of a name the spec does not give).
- Conversion: the spec allows server-side resample/PCM conversion "only where
  stdlib can do it" for vendors that need another format. None of the vendors
  Hello gives a custom tone has a documented other format in the spec, so none
  is done; a converter would be unused code. Revisit with the lab run.
- The tone URL is built from the phone's `Prov.URL` in the template, so
  previews mask the token as they do for the firmware URL.
- API: `GET|POST /api/v1/ringtones`, `DELETE /api/v1/ringtones/{id}`; operator
  to write, viewer to read; upload is multipart and excluded from MCP like the
  other uploads; list and delete are MCP tools. UI: a Tones tab on Ringing.

## Queue callback (pbx-gaps wave B, B4)

- **Merged gaps-languages.** PR #208 (voicemail box languages, `voiceprompt.Resolve`) was not on main, so origin/gaps-languages is merged into this branch. The queue's language is `Resolve(queue.prompt_language, deployment language)`; `queues.prompt_language` is NULL for inherit, no CHECK (validated by the API), migration 00049.
- **The callback call is a queued caller, not a new call type.** The customer's answered dialog is turned into the only dialog of a call built like one a survivor rebuilds after a takeover (homed solo call, phase `queue`). It then runs `queueResume`/`queueRun` with `EnteredMs = position_key`, so ordering, music, offering, bridging (`ivrConnect`) and failover all reuse the queue code, and the caller is rung before any agent by construction. `originateFor` was not used: it needs an inbound caller dialog.
- **HA.** The replicated queue detail gains `:cb<id>`. A takeover re-homes the customer and the survivor renews (adopts) the lease. A node lost before the customer answers leaves the row `dialling`; the 60 s lease lapses and any node claims it again (counts as an attempt; a lapsed third attempt becomes `failed`/`node_lost`).
- **Ordering.** A sweep claims the head request only when no waiting caller with an earlier place is unserved and free agents exceed live claims. Waiting callers are held back from an agent while a request with an earlier place is due or being served (`Due`, one query per node per sweep, cached; PostgreSQL errors clear it, so calls are never held for a store that is down). A caller who entered later may still take the last free agent during the seconds the customer's phone rings; accepted.
- **Source of the origination.** Outbound routing needs a source extension: the agent the strategy would ring first. The caller is presented as the queue (number, else name). Trunk candidates are tried in route order exactly as `setupOutbound` does (slot per trunk, destinations not known down, failover on the route's failover codes or a silent attempt, no failover once a trunk rang and nobody answered or on a code outside the list); each trunk attempt is its own leg stage so a given-up leg's late events are ignored.
- **Dialable number.** `^\+?[0-9]{2,20}$` after dropping spaces, dashes and brackets; used as the caller presented it (no national/E.164 normalisation, which needs P-10/`HELLO_DIAL_*` work not on main).
- **Caller name.** The row carries the name the call already has. The shared-book lookup (phonebooks-ptt P-10) is not on main; when it lands the same field carries it, and a personal book never names a queue caller.
- **Callback in the wait.** The wait and maximum wait of a called-back caller count from when it answered; its place is the original one. Voicemail breakout is off for it. No agent within the maximum wait is a failed attempt (`no_agent`), retried.
- **Offer threshold.** 0 offers at once. With nobody staffed the time waited stands in for the estimate. The default-key offer text is pre-generated; other keys are synthesized on first use.
- **CDR.** No `disposition` column exists; `callback_failed` is a queue leg with the attempt's result, and the call's kind is `callback`. Every attempt that dialled has its own CDR. `callback_requested` is on the original call's CDR, whose exit result is `callback` (no abandon).
- **Cancelling** a request that is dialling stops it: the node holding the call checks the row every 2 s (the lease renewal doubles as the check; a renewal that finds the row no longer dialling means cancelled). While the customer's phone rings the leg is cancelled; a customer already on the line is hung up. The CDR gets `callback_failed` with result `cancelled`, the row stays cancelled and no attempt is counted or retried.
- **Agent reservation (chosen: lease, plus the existing hold).** When the sweep claims a request it takes the agent lease of the first agent of the strategy's plan that is not leased to another caller (none: no dial), for ring time plus 30 s, and gives it back as soon as the customer answers, so the queue can offer it. That blocks a later waiting caller on any node at once, whereas the earlier `Due` hold map is refreshed every sweep (3 s) and only orders callers against due requests. Skipping ahead was rejected: the order is already by place, and the problem is the window, which a lease closes.

## SIP over TLS terminates at the edge (2026-10-26)

Phone-feature-parity S-14 (delivery PR 23) says "hello-sip listens on TLS 5061".
Hello's phones already reach Kamailio, not hello-sip, so Kamailio (tls module)
terminates TLS and the hello-sip nodes keep UDP inside the edge network.
Kamailio reports the phone leg's transport in `X-Hello-Transport` (udp, tcp, tls;
trusted proxies only), the registrar stores it on the binding, and the `sips:`
rule uses it: a `sips:` request needs a TLS caller and rings only TLS bindings,
and is refused with 480 otherwise, also for trunks and external SIP URIs. TLS is
off until `KAMAILIO_TLS_PORT` is set, so an image without `tls.so` still starts.
Provisioning points the models in `HELLO_PROV_SIP_TLS_MODELS` (default
`grandstream:WP`) at `HELLO_PROV_SIP_TLS_SERVER`; Snom has no confirmed transport
key and stays on UDP. The `HELLO_SIP_TLS` and `HELLO_SIP_TLS_CERT` settings of the
spec's config list are not used: the certificate belongs to Kamailio. Later PRs
(SRTP, DTLS-SRTP) offer crypto only on legs where `bindingOnTLS` or the caller's
`clientTransport` is TLS.

## Transfer-to-voicemail phone key (2026-10-26)

Yealink, Snom, Fanvil and Poly templates provision a key that transfers the
active call to `*97<own extension>` (`internal/prov/keycaps.go`, `PlaceVMKey`).
Choices:

- The target is the phone's own extension. A key cannot ask for a number, and
  "send this caller to my voicemail" is the common use; a colleague's box is
  still reachable by dialling `*97<ext>` after a transfer.
- The key goes on the first key left after the BLF keys, never ahead of one.
  A phone whose keys the BLF keys fill gets none (no BLF is dropped for it).
- Written only when the PBX defines the voicemail feature code and the
  extension has voicemail enabled (`Line.NoVoicemail`, from
  `extensions.voicemail_enabled`).
- Gated like BLF: Yealink, Snom and Fanvil only on a model in the capability
  table; Poly on every model (a softkey); no Grandstream, since a WP has no key
  block in its template and the P323 block of the other models is unverified
  and overlaps the phonebook keys.
- Every key type is unverified on a phone and marked "(verify)" in the templates
  (Yealink type 3, Snom `transfer`, Fanvil memory key `/f`, Poly efk macro).
  The provclient Fanvil parser now counts only `/b` memory keys as BLF.

message. `conference` was left for a follow-up (see below).

## Routes form: conference destination (2026-10-26)

The inbound route drawer also offers `conference` ("Conference room"). The target
is chosen from the rooms list (`GET /api/v1/conferences`) and stored as the room
number, labelled "number · name"; a stored number the list no longer has stays
selectable as "(not found)". An empty target is refused with "Choose a conference
room." This closes the gap noted for flow canvas P1.

## Flow canvas P3: the batch backend (2026-10-26)

Built per `.procoder/specs/flow-canvas.md` (P3). Choices the spec left open:

- **Store shape.** The spec's `store.WithBatch(ctx, tx)` is `Store.RunBatch(ctx,
dryRun, fn)`: it opens the transaction, takes the configuration lock, reads
  the revision and the baseline routing configuration, and calls `fn` with a
  context that carries the batch. Every store write and read made under that
  context uses the batch's transaction (`Store.q(ctx)` replaces `s.db` in the
  configuration files, `Store.tx`, `configChangeID` and `provChange` join the
  batch), so an operation sees the ones before it. `configChangeID` then skips
  the lock, the per-operation check and the revision bump and records which
  check functions the batch used; `RunBatch` runs them once at the end against
  the baseline, bumps the revision once and notifies once. The audit via is
  `batch:<uuid>`; the outer origin goes on the `config_batches` row.
- **The batch table** is `00046_config_batches.sql` (`ops`, `results`,
  `reverts`, 90 days, pruned on insert). `ops` holds the operations as they ran
  with credential values withheld; `results` holds, per operation, the object
  before and after as its GET returns it, the diff and, for a delete, the
  node's canvas positions.
- **Which operations.** An explicit table (`internal/configbatch/ops.go`), not
  "everything with write scope": the object writes of the canvas. Refused with
  `400 op_not_batchable`: operations whose response carries a secret
  (`createDevice`, `createPhone`, so devices and phones are updated and deleted
  in a batch but not created), credential scopes (`secrets`, `session`),
  multipart, and anything that touches MinIO, a speech provider, a phone or
  Valkey state (announcements, greetings, firmware, queue agent login,
  conference control, calendar import, the time condition override toggle).
  Admin-scoped object writes (paging groups, conferences, multicast channels)
  are allowed: the inner operation still needs its own scope and role.
  Decision 2 lets credential operations into the AI path once their secret is
  stripped; that belongs to P6 and is not done here.
- **Announcements are not batchable** (audio is in MinIO, generation calls a
  provider). A batch can point at an existing announcement (an IVR greeting)
  but not create one; the canvas links to the Announcements page.
- **`baseRevision` is required.** `expect` is the node `version` of the graph
  (hash of the stored object) and is checked only when the revision moved; an
  operation without `expect`, or aimed at an object the batch creates, never
  conflicts. Revert uses its own stricter check (below).
- **Errors.** The first failing operation stops the batch (later operations
  depend on earlier ones), so `errors` has the failing operation's fields, not
  one entry per operation. A refused operation is `403` with the inner code
  (`forbidden_role`, `insufficient_scope`) and its index in the message. Any
  other non-success of an operation (400, 404, 409) is `422 validation_failed`
  with `errors: [{opIndex, node, path, message}]`. The final routing check names
  the operation that changed the item the error is about, or `-1`.
- **Placeholders** take any field of the referenced response (checked against
  the operation's response schema before anything runs). A string that is
  exactly one placeholder keeps the value's type.
- **Nested replay.** `replay.Request.Inherit` lets the batch replay its
  operations inside an MCP replay (the batch route is itself called as an MCP
  tool); the outer replay marker, and so the OAuth client, is kept.
- **Layout in a batch.** `canvasId` plus `layout` places new nodes (the node id
  may hold a placeholder); every applied batch prunes layout of nodes that no
  longer exist on every canvas. A layout write moves `layout_version` and is
  audited as `flow_canvas layout`, and is not a configuration change.
- **Warnings** in the answer are the flow warnings the batch introduces (after
  minus before), not all of them.
- **Revert.** The inverse is built from `before`: a PATCH restores only the
  keys its body set (a key absent in `before` is sent as null); a PUT or create
  sends the writable fields of the GET (pruned to the request schema, which also
  drops read-only fields); credentials a batch set are never stored, so they are
  listed in `skipped`. A deleted object is made again with a new id and later
  operations of the same revert use it. The staleness check compares, for the
  last operation that touched each object, the writable fields of its recorded
  `after` with the object now (fields outside the request schema, such as
  timestamps and live state, are ignored). A batch reverts once (`409
conflict`), and a batch that deleted a device or phone (which a batch cannot
  create) is `422`.
- **TestEdgeRoundTrip** exercises every connection the catalogue offers from a
  stored "call goes to" field (inbound route, ring group, IVR keys, timeout and
  invalid, queue exits, time condition branches), reading the target kinds from
  the catalogue so a new connection fails until it has a fixture, plus the
  member ports (`TestEdgeRoundTripMembers`). The `internalDid` target is not
  exercised (its target is the route's own DID; the golden graph covers it).
  Resource edges that are plain id lists (outbound trunks, hold music, schedule
  calendars, device and phone binding, prompts) are drawn from the same
  `Owner` field table and covered by the golden graph; their batch operations
  are the ordinary updates.
- **Metrics:** `hello_config_batches_total{result}` and
  `hello_config_batch_ops`. The graph build and trace metrics belong to their
  phases.

## Flow canvas P4: the editing UI (2026-10-27)

- **The draft is a list of batch operations, not a copy of the graph.** Each
  staged gesture becomes `DraftOp`s (`web/src/pages/flow/draft.ts`): the
  operation, its `expect` version and the versions of every node it read.
  Edits to the same object merge into one operation (key `upd|<node>`), so two
  changes to one ring group, or a rename and a referrer update that touch the
  same IVR, are one `PUT` and cannot overwrite each other. All feature-code
  edits share the key `list|putFeatureCodes` (one `PUT` of the whole table).
  An edit of a node that only exists in the draft is folded into its create
  operation, and deleting such a node forgets it and its edits.
- **The canvas draws the draft.** `project(graph, draft)` returns the graph as
  it will be once applied (new nodes, deleted nodes, rewired edges, renamed
  labels); the canvas, the panel and the pickers all read that, so what the
  user sees is what the dry run checks.
- **The catalogue decides, objects.ts only says how.** Whether an output may
  connect to an input is `canConnect` over the catalogue's target lists
  (the UI never hard-codes compatibility, spec constraint). `objects.ts` holds
  only how an allowed connection is written into the owner's body (an adapter
  per output port) and which settings the panel edits. A connection with no
  adapter (outbound trunk order, hold music, prompts, device binding, schedule
  calendars) is drawn but its handle is not connectable and the panel says to
  use the object's own page. This is scoped to the "call goes to" fields,
  forwards, group and queue members, inbound trunk, feature-code targets and
  the IVR keys, which is what spec S-10 to S-12 name.
- **PATCH sends only what changed; PUT sends the whole object.** The base of a
  PUT (IVR, queue, time condition, feature codes) is the GET answer pruned to
  the request's writable keys; PATCH objects (extension, ring group, inbound
  route) send only the changed fields, so the id and timestamps never reach an
  `additionalProperties: false` body.
- **Names, not ids, in destinations.** Destinations store the target's name,
  number or code, so a rename stages the renamed object plus an update of
  every referrer (found from the graph's edges, written through the same
  adapters) in one gesture. A referrer that cannot store the new name refuses
  the rename with the reason. A forward to a ring group whose name is not
  numeric is refused with the reason (spec edge case).
- **Delete lists referrers and clears them first.** `referrersOf` walks the
  edges that point at the node and are owned by another object; the confirm
  dialog lists them; the delete stages the referrer updates, then the delete.
  A referrer that cannot be cleared (an inbound route always needs a
  destination) blocks the delete and is named.
- **Gestures stand or fall together on a rebase.** Every op carries the ids of
  the gestures that staged it. On a revision change `rebase` drops an op whose
  touched node changed or vanished, and every op that shares a gesture with a
  dropped one (a rename without its referrers would leave them dangling). The
  dropped changes are listed in a warning; nothing conflicting is kept. History
  is cleared by a rebase, since old steps would bring back what was dropped.
- **Live validation is the server's dry run.** 500 ms after the last change the
  whole draft is sent with `dryRun: true`; `422` problems are drawn on their
  node (the `node` of the problem, else the op index), a `409 config_stale`
  reloads the graph (which rebases), and Apply is enabled only once the check
  answers for the current draft. "Checking" is derived (the check records the
  draft it was for), not stored.
- **Apply always goes through the review dialog** with the dry run's
  field-level diff and warnings; the only apply call is the dialog's confirm.
  On `409 config_stale` at apply nothing was applied: the dialog closes, the
  graph reloads, the draft is rebased and the dropped changes are reported.
- **Layout of new nodes.** A new node has the id `<kind>:~<ref>` in the draft
  and a position in the layout reducer, but the layout PUT skips it. At apply
  its position goes in the batch's `layout` as `<kind>:${ref.id}`, so the
  server places it once the id exists. A discard or a rebase forgets it.
- **The draft is kept in `localStorage`** under
  `hello.flow.draft.<canvasId>.<user>` as `{baseRevision, ops, history}` and
  rebased when the canvas opens on a newer revision. Positions of draft nodes
  are not kept; they come back in "Not on this canvas".
- **Undo and redo restore the exact op list** (a snapshot per gesture, 60
  deep) and are bound to Ctrl/Cmd+Z outside text fields. A new gesture ends
  the redo line.
- **Accessibility.** Every connection can be made in the panel (a select of the
  catalogue's targets per output, a key field for IVR keys, a typed external
  number or SIP URI) and removed with a Disconnect button; the pointer drag
  and the Delete key on a selected edge are shortcuts to the same operations.
  Objects are not deleted with the key: they have a confirmation.
- **Forms are shared (spec S-15).** The create/edit drawers of ring groups,
  IVR menus, queues, time conditions, conferences, inbound and outbound routes,
  extensions and the text prompt editor moved from `web/src/pages/*.tsx` to
  `web/src/forms/*Form.tsx` unchanged (their page tests pass unmodified). Each
  drawer that the canvas uses got one optional prop, `commit(body)`: when set
  the drawer hands its validated body to the caller instead of calling the
  API. The canvas panel's "Edit all settings" opens the same drawer
  (`FormHost`) with the object's GET (plus what the draft already changes) and
  stages the result with `stageBody`.
- **`stageBody`** keeps only the fields that differ from the object as it
  stands, runs the rename cascade when the name changed, and recomputes the
  edges the new values draw (`wanted.ts` reads the stored fields the way the
  graph builder does, per owner-field group; groups it does not cover are left
  alone) so the projected graph is right without a round trip.
- **Every connection with a stored form is wirable.** Adapters were added for
  outbound trunk order (`trunks`, ordered ids), hold music (`mohClassId` on
  extension, ring group, inbound route and queue), prompts (IVR
  `greetingId`/`invalidPromptId`/`timeoutPromptId`, queue prompt ids,
  conference entry/exit announcement names, hold music `announcementIds`),
  device to extension (`extensionId`), phone to device (`deviceId`), calendars
  to schedules (`calendarIds`), schedule to time condition (`scheduleId`),
  paging group multicast channel and paging members/callers. All use existing
  batchable operations, so no backend change was needed. Required fields
  (an IVR greeting, a device's extension, a time condition's schedule) can be
  re-pointed but not cleared; the panel says why. None of these truly cannot
  be stored.
- **Draft node positions** are persisted with the draft
  (`{baseRevision, ops, history, positions}`) and restored into the layout
  reducer on load, for nodes whose create operation survived the rebase.
- **Generate prompt** (tts-prompts is on main, PR #139): the panel's
  announcement section and a toolbar button open the Announcements page's
  prompt editor (`PromptHost`). Generation calls the speech provider and
  stores audio, which a batch cannot roll back, so it runs immediately and is
  not staged; the canvas then reloads and places the announcement.
- **Still out of scope.** Creating devices, phones and trunks with credentials
  (spec). The extension drawer is shared but not wired to `commit` (its
  voicemail, device and live panes act on the server at once); the panel edits
  an extension's name and DND and its forwards and hold music as connections.

## DSCP marking, parity item 28 (S-21) (2026-10-27)

Built to the spec: SIP socket DSCP 26, relay/anchor/fanout/party RTP sockets
DSCP 46, `HELLO_DSCP_SIP` / `HELLO_DSCP_RTP` (0 to 63, validated), Grandstream
P1558 / P1559 from the same two values.

- RTP DSCP is a process-wide setting (`media.SetRTPDSCP`, default 46) read at
  every bind, not a parameter of each constructor: one value per process, and
  the six bind sites keep their signatures.
- hello-control reads the same two env vars so phones are provisioned with the
  values hello-sip marks with; set both services alike.
- A refused mark (platform, sandbox) is logged once and never fails SIP or a
  call.
- Originated SIP and the mark. Verified by test: sipgo copies the client's
  forced local address (the listening socket) into a request only when no
  request options are passed. The six call sites in `edge.go` and
  `takeover.go` that pass `ClientRequestAddVia` got an ephemeral socket of
  sipgo's own (v1.6.0 has no socket-control hook or dialer Control), which
  nothing can mark and which was not on the SIP port. They now use
  `Server.addViaFromListener` / `fromListener`, which set the request's local
  address to the listener, so all originated SIP leaves from the marked
  socket (`TestDSCPOutboundOnMarkedSocket`). A forked sipgo was rejected as
  too heavy for this.
- Other vendors, from the vendors' administrator guides (nothing in this repo
  documents them): Yealink `static.network.qos.signaltos` / `audiotos`, Poly
  `qos.ip.callControl.dscp` / `qos.ip.rtp.dscp`, written like Grandstream,
  unverified on hardware. Snom and Fanvil: no documented DSCP provisioning key
  could be confirmed (snom uses TOS-byte settings whose exact names are not
  in this repo; Fanvil's are device-specific), so none is written rather than
  guessed. Revisit with the vendor guides. Site overrides go through S-12.
- No migration, no API or UI change; TCP/TLS SIP is not served by hello-sip.

## Flow canvas P5: traceFlow and trace mode (2026-10-27)

- **Where the walk lives.** `flow.Trace(Input, TraceStart, TraceOptions)` builds
  the graph and walks its edges, so the trace can only name a node and port the
  canvas draws. The API handler decides the call (`routing.Decide`, as
  `routingTest` does) and turns the decision into the start: the trunk step,
  the matched inbound route step, then `Follow` (an output port) or `Enter` (an
  input port); an outbound hop is appended as `Tail`. `internal/flow` does not
  import `routing`.
- **Step contract.** `{node, port, text, at?, branches?}`. `port` is the
  output port the call leaves the node through; the canvas highlights the graph
  edge from `(node, port)` to the next step's node. A last step's `port` is the
  input it ends at (`voicemail`). Steps on the same node (an extension rings,
  then its voicemail) draw no edge.
- **Which inbound route matched** is read from the first `Route "x" matched`
  line of the routing trace, because an `external` destination overwrites
  `Decision.Route` with the outbound route's name.
- **Numbers answered before routing.** From an extension, hello-sip answers a
  paging code, a conference room, an IVR number, a queue number and a ring
  group name before it asks routing (`b2bua.go`). The trace repeats that order,
  then falls through to `routing.Decide`. Park retrieval, feature codes and
  `Replaces` pickup are not traced.
- **Extensions.** `forwardAlways`; do not disturb goes to voicemail when the box
  is on, else the call is declined (as `failoverExtension` does); otherwise it
  rings and then the no-answer forward or voicemail. Whether the extension is
  busy or has a registered device is live state and is not read: the ring step
  carries two branches instead, like IVR keys. "If busy" takes the busy
  forward, else the voicemail, else the caller hears busy; "If not registered"
  takes the voicemail, else the call is answered temporarily unavailable (the
  rules of `callflow.go` and `failoverExtension`).
- **Feature codes and park slots** dialled from an extension are traced after
  the queue number check and before ring group names, the order of `b2bua.go`.
  The longest matching code wins; what follows it is the argument. Codes that
  name a target (IVR, announcement, time condition) go to it, from the feature
  code node when the code row stores the name (the graph edge), else from the
  caller. Voicemail (`*97` own box, `*97<ext>` leave a message), intercom,
  queue login/logout/pause, time condition override, park retrieve, pickup and
  the settings codes (forward, DND, call waiting) end in one step that says
  what they do to the caller's own extension; park and the transfer codes
  dialled as a call are rejected (403).
- **Live state** (park slots, queue agents) is read by the handler (`s.Parked`,
  `s.Queues`, 2 s timeout) and given to the walk as `flow.Live`. Park retrieve
  and a dialled slot say whether the slot holds a call and who parked it. A
  queue lists each agent (static agents are available unless paused; dynamic
  ones need a login to this queue) and takes the `timeout` exit when someone is
  staffed, else `noAgents`. What cannot be read (reader not wired, error, or
  timeout) is shown as "unknown (live state unavailable)", and the queue then
  has two branches, "If an agent is available" and "If no agent is available".
  The agent codes also say whether the caller's extension is logged in or paused.
  Whether an agent's phone is busy, in DND or registered is not judged for a
  queue (the SIP node does), only the shared login and pause state.
- **Ring groups and queues** follow the failure path (`noAnswer`; for a queue
  `timeout`, or `noAgents` when it has none): a member or agent who answers ends
  the call, so the trace shows what happens when nobody does. The step text
  says so.
- **IVR.** The first key of `digits` picks the option (`port key:<d>`); the rest
  go on to the next menu. A key that is not an option takes `invalid`. With no
  keys left, the menu step carries a branch per key and for `timeout`
  (`invalid` is not listed). "To depth 6" is six nested menus expanded; deeper
  ones are not expanded and the step says so. A loop (same node, port and
  remaining keys twice on one path) ends the path with a step that says so; a
  path is cut at 64 steps.
- **Time conditions** use `timecond.Resolve` on the conditions built the way the
  store builds them for the evaluate endpoint, at `at`, override included.
  Each condition in a chain is one step with the branch as its port (a holiday
  with no holiday destination takes `closed`, the edge the graph draws). A
  forced branch says so and who set it and when; the override has no expiry in
  the data, so the "until" wording of the spec is not shown. An announcement
  followed by a "then voicemail" box adds a voicemail step.
- **Endpoint.** `GET /api/v1/flow/trace`, `traceFlow`, read/viewer (unlike
  `testRouting`, which is write/operator: it reads the configuration only and
  places no call). Answers `{configRevision, at, from, number, outcome, route,
reason, steps, routingTrace}`. A rejected call (no route matched, outside an
  inbound schedule) is `200` with `outcome: reject` and a final step on the
  trunk. Trunk usability uses the same live-state rule as `routingTest`. The
  metric is `hello_flow_trace_total`, registered with the batch metrics.
- **Canvas.** Trace mode is a button in the canvas toolbar (viewers too) that
  swaps the node panel for a trace panel: source, number, caller ID, time
  (`datetime-local`, empty is now), a keypad, and the result list. Highlighting
  is done by two pure functions (`traceHighlight.ts`) that mark the view
  after `buildView`, so the page keeps one extra line of state and the P4
  editing code is untouched. Traced objects that are not on the canvas are
  counted in a note, not drawn.
- **Not in this change** (their phases): `traceFlow` in `assistantTools` and the
  skill (P6).

## HT813 FXO disconnect supervision: where to apply it

Implementation notes (template route, no migration):

- Source for every key: Grandstream ht813_config_1.0.19.6.txt (config-template-10212026.zip), section "FXO Termination Settings" in the FXO PORT block. The FXS-side P892/P856/P205 (loop-current/polarity on the FXS port) are a different feature and are not written.
- P893 = 1: Enable Current Disconnect (loop-current drop).
- P764 = 100: Current Disconnect Threshold, 50-800 ms, Grandstream default 100 ms; kept at the default.
- P28101 = 1: Enable Polarity Reversal (template default 0).
- P712 = 1: Enable PSTN Disconnect Tone Detection.
- P841 = f1=400@-32,c=375/375; : PSTN Disconnect Tone, UAE busy tone 400 Hz, 375 ms on/off. Format and -32 dB level from the template's example (f1=480@-32,f2=620@-32,c=500/500;).
- Unconfirmed, left out: a silence-timeout backstop (the HT813 template has no such key; P3630 is only the PSTN ring timeout for unanswered calls, P4421/P28761 are hard call-duration limits) and the UAE congestion/disconnect tone (no Grandstream source; a single P841 tone only).
- Applied through Phone.FXO (model HT813), not a new WP family, so the generic Grandstream key set is unchanged. TR-069 untouched.

## 2026-10-29 FXO line busy after a voicemail call (fix-trunk-slot-leak)

- **Finding.** The trunk slot is a Valkey sorted set with a 30 s TTL per
  member that the call heartbeat refreshes, so a slot cannot leak
  persistently on its own; production's cluster-wide `hello_trunk_active_calls`
  is 0. "full" answers the caller 503, not 486: the 486s are the HT813
  itself ("rejected"). No migration and no startup sweep are needed.
- **Real defects fixed.** (1) `end()` stopped closing `stopHB` when Phase 7
  swapped it for `haStop`, so every ended call leaked its heartbeat goroutine
  and the voicemail recorder never saw the hangup. (2) A refresh racing the
  release re-added the freed slot (counts until its TTL); now released again.
  (3) An Opus decoder that hit the library assertion stayed broken for the
  whole call; it is rebuilt after a panic or 5 refused packets in a row. The
  music encoder was also created outside the stack pin.

## Dial-by-name directory (pbx-gaps B3, S-13) (2026-10-27)

- A new destination kind `directory` with an empty target, accepted by every
  output that stores a call destination: inbound routes, ring group fallback,
  IVR keys / timeout / invalid, time condition branches, queue exits (decision
  3), plus the feature-code action `directory`. Migration 00048 (renumbered by
  the merge captain) widens the CHECK lists additively with
  `pg_temp.hello_check_values`; down removes or hangs up the rows of the kind.
- Default feature code is `*42`, not `*411` (D11). `*411` extends the default
  directed-pickup code `*41`, and the dial-space guard refuses a code that is
  a prefix of or prefixed by another, so it could never be seeded. The action
  is configurable, so an installation that frees `*41` can use `*411`.
  Reported to the coordinator: the same clash exists for follow-me `*74`/`*75`
  against park.
- The index is built into the snapshot (`Snapshot.DirectorySearch`), with
  `directory_hidden` carried in the snapshot's extension row. Matching is by
  keypad spelling of the first or last word of the extension name (the
  extensions book has one name field); accents fold; digits 0 and 1 carry no
  letters. More than 7 keys are refused with an extra prompt key
  `directory.toolong` (the spec lists six keys; "asks for fewer" needs its own
  sentence).
- Connection uses the IVR's own dial path (`ivrDial`), not a REFER: the caller
  is a one-legged B2BUA call on the relay, so putting the extension in front
  of the relay gives the "blind transfer" result, keeps the CDR as one call and
  applies the extension's own forwarding and voicemail.
- The one connected name is the voicemail box's recording (`name_object`)
  when present, read with one box lookup at hand-over time as the voicemail
  application does; otherwise spoken with the Speaker (the text-to-speech
  cache over go-ai-sdk). `directory.connecting` is "Connecting you to" with no
  name placeholder so the recording and the spoken name both follow it.
- A call that reaches the directory without a menu (route, time condition,
  group fallback, `*42`) uses phase `directory` for HA: a taker restarts it
  from its first prompt. Inside a menu the menu's phase is kept.
- Canvas: one pseudo-terminal node `directory:names` that every output kind
  and the feature code draw an edge to.
- S-20 for menus: `ivr_menus.prompt_language` (NULL inherits), resolved with
  `voiceprompt.Resolve` as voicemail boxes are; API `promptLanguage`, console
  select on the IVR form. Merged `origin/gaps-languages` (PR 208) for
  `voiceprompt.Resolve`; the PR depends on it.

## Phone resync/reboot rejected (fix-phone-resync, 2026-11-09)

- Root cause: a Grandstream challenges the check-sync NOTIFY in realm
  "<SIP server host:port>" (kw: 192.168.10.101:30508), not the SIP domain.
  `answerChallenge` only accepts the device realm (the stored HA1 is bound to
  it), so every challenged push became `auth_failed` ("needs one manual
  reboot"). hello-sip-1 logged "challenge realm ... is not the device's realm".
  Not the Kamailio pair: NOTIFY is routed by the binding's Path, and the
  device registered through the replica that holds the flow.
- Fix: when the queue can open the device's sealed password
  (`DeviceSecrets`, `Store.DeviceSecret`), hash it for the realm the phone
  named and answer. The password is never logged. Phones need no change.
- Rejected: P4428=1 (no NOTIFY auth) would let anyone on the path reboot a
  phone and needs a phone reboot to load.

## Queue callback merged onto main (merge captain, PR 237) (2026-11-09)

- Main moved the queue drawer to `web/src/forms/QueueForm.tsx` (flow canvas
  P4), so the callback fields and the prompt-language select were ported into
  that file. The Callbacks link in the queue card stays in `Queues.tsx`.
- `migrations/00047_prompt_language.sql` was identical on main, so the
  duplicate in this branch adds nothing. The queue callback migration is
  `00049_queue_callback.sql`; the migrate counts (34/35/37) cover 00026 to 00049.
- Merged the counter and log of the answered queue leg from main and the
  callback finish from this branch in `internal/sip/queue.go`; both run once.
- Metric lists in `internal/sip/metrics.go` keep both sides' collectors.

## Completion plan 2026-11-09: how to run it

- Start: run waves 0-6 in order, three or four builder agents in parallel plus the merge captain (recommended)
- Start with wave 0 and 1 only, then review
- Item 27 provisioning hardening: include turning TR-069 off, or leave TR-069 alone

## Follow-me (pbx-gaps B1: S-5, S-6) (2026-11-09)

- **Feature codes are `*76` (on) and `*77` (off), not `*74` and `*75`.** Those
  are the default park and park-retrieve codes (migration 00032); the dial-space
  guard refuses a code that equals or extends another, so the spec's codes could
  never be seeded. The actions are `follow_me_on` and `follow_me_off`, so an
  installation can move them; migration 00052 seeds them on existing installs
  when free, `DefaultFeatureCodes` on fresh ones. The spec text is corrected.
  A code dialled by an extension with no profile that has a target answers 404
  and changes nothing.
- **Migration 00052** (highest on main was 00050): `follow_me`,
  `follow_me_targets`, the two feature-code actions, and `cdr_legs`. No branch
  has `cdr_legs` yet (A4 landed as `cdrs.codec` only), so 00052 creates it as the
  spec's S-22 shape (`CREATE TABLE IF NOT EXISTS`) plus two additive columns,
  `result` and `target_kind`. Follow-me writes the role `follow_me`; the codec
  columns stay NULL for it. Down removes the follow-me rows and the two columns,
  and leaves the table. The migrate test counts are 39/36/37.
- **Where it runs.** In `ringExtension` (`internal/sip/callflow.go`) after
  forward-always and before the DND, busy, unreachable and no-answer handling,
  and in `ivrRingExtension`, the same step for an IVR option that dials an
  extension. The ring is its own loop (`followMeRun`) because the generic
  setup loop cannot start legs on a schedule, but it reuses the legs, the
  event channel, the trunk slot and outbound decision of a forwarded external
  number, the ring group members, and `exhausted` for the no-answer forward and
  voicemail, so an unanswered follow-me ends exactly where a plain ring ends. It
  is not applied to ring group members or to a group or queue failing over to
  an extension (they ring phones directly), reported in docs/follow-me.md.
- **Strategies.** Simultaneous rings own phones and all targets at once.
  Sequential keeps the own phones ringing and starts the targets in order; a
  target's `delay_seconds` is how long it rings before the next starts, the last
  one rings to the timeout, 0 on a non-last target means until it fails. In an
  IVR dial-out the ring primitive cannot stop one leg, so earlier targets keep
  ringing there (documented).
- **DND.** `dnd_blocks` (default on) skips follow-me and the ordinary DND
  handling runs. With it off, DND only silences the own phones.
- **Targets.** An extension target rings that extension's phones directly (its
  DND skips it; its own forwarding does not apply), a ring group target rings
  the eligible members at once, and the first usable trunk of an external
  target's route carries it. A number that routes to one of our own extensions is
  treated as that extension. Loops: the extension itself or one in the forward
  chain is skipped. A leg that goes through a carrier and comes back is a new call
  and is not detectable; documented as the carrier's hop limit.
- **Press 1.** The answered external leg is held before it may claim the call
  (`leg.screen`, run by `fmScreen`): the relay's callee leg is aimed at it, both
  directions are muted, the prompt `followme.accept` (new key, English and
  Dutch) is played with the call's Speaker (a beep without one), and the digit
  `1` (RFC 2833 on the relay, or INFO) connects it; 10 s after the prompt without
  the digit drops it. One leg is asked at a time. A call with no SDP offer has no
  media anchor, so its external targets are skipped with a trace line. The Dutch
  wording needs a native-speaker review before merge (S-21).
- **CDR.** `cdrs` is unchanged; each leg is a `cdr_legs` row (role `follow_me`,
  `target_kind`, extension or number, trunk, result). `GET /api/v1/cdrs/{id}`
  gains `followMeLegs`. The insert statement gains one CTE and one argument. When
  an external leg wins the call's trunk, route and direction are that leg's.
- **API.** GET is read-scope/viewer (the route table test ties the read scope to
  the viewer role; the spec said operator), PUT is write/operator, and the same
  pair under `/me/extensions/{id}` for a user. A save names the fields it changes;
  `targets` replaces the list; enabling needs a target; the store re-checks
  self-target and existence, and refuses deleting or renumbering an extension or
  group a profile names (409).
- **HA.** The profile is in the snapshot every node loads, a change goes through
  `configChange` (revision bump and NOTIFY), and an answered follow-me call is
  replicated like any answered call, including one an external target answered.
  A ringing call and a leg being asked to press 1 are not replicated, as for every
  ringing call.
- **Tests that need real hardware or a carrier** stay on the lab list (item 4 of
  the spec): a mobile on a real carrier with press 1.

## Multiple lines and account sharing (parity item 21, S-10 and S-11) (2026-11-09)

- **Data model.** Line 1 stays `phones.device_id`. Lines 2 to 6 are rows of
  `phone_lines (phone_id, line, device_id UNIQUE)` (migration `00052_phone_lines.sql`).
  A device is one line of one phone (the store refuses a device that is line 1
  or another line of any phone, and a second line of a phone on the same
  extension). One extension on several phones (the shared line appearance) is
  each phone binding a device of its own on the extension; hello-sip already
  forks a call to every device of an extension, so no SIP change was needed for
  it. No bridged line appearance (dialog-state mirroring) is built: S-10 and S-11
  do not ask for it.
- **API.** `PUT/DELETE /phones/{id}/lines/{line}` (`setPhoneLine`,
  `clearPhoneLine`), `lines`/`maxLines`/`sharingRole` on the phone,
  `PUT /phone-groups/{id}/account-sharing`. Line 1 keeps `extensionId`/`deviceId`
  on create and update, so existing clients do not change.
- **Line counts** (`prov.MaxLines`): Grandstream from the account blocks of its
  own templates (config-template-10212026.zip, checked 2026-11-09): WP8x6 3
  (`wp8x6_config_1.0.3.39` has accounts 1 to 3; its header labels account 3
  "GRP2602/2603/2604 only", a template shared with that family, while the
  account-sharing and default-account choices list Account 3 and the spec gives
  the WP8x6 three: confirm the third on a handset), WP810, WP820, WP822 and
  WP825 2 (`wp810_822_825_config_1.0.11.83` and `wp820_config_1.0.7.90` have
  accounts 1 and 2), WP856 6 (`wp856_config_1.0.3.16` has accounts 1 to 6; this
  was 1 before, for lack of a template); other Grandstream models 1. Yealink
  and Snom the line-key count of the key table capped at 6; Poly and Fanvil 6.
  The cap of 6 is Hello's, not a vendor figure. A model change that would leave
  a phone with more lines than the new model takes is refused with 409.
- **Keys written: only those the repo's templates already confirm.**
  Yealink `account.N.*` (the existing Yealink template states another account
  takes the same keys), Poly `reg.N.*` and `msg.mwi.N.*`, Snom `idx="N"` on the
  per-identity settings, Fanvil `SIPN ...` (the Fanvil template names SIP2, SIP3).
  Each is marked unverified in the template.
- **Grandstream lines are written** (reversing the earlier "held, not
  written"; the templates were not in hand then). Account N of the
  four WP templates starts at `b = 201 + 100 N` (account 2 `P401`, 3 `P501`, 4
  `P601`) and at `b = 1201 + 100 N` for the WP856's accounts 5 and 6 (`P1701`,
  `P1801`). Inside it, all confirmed in the templates for every family and
  account (checked programmatically against each file): `b` Account Active,
  `b+1` SIP Server, `b+3` SIP User ID, `b+4` SIP Authentication ID, `b+5` SIP
  Authentication Password, `b+6` Name, `b+11` Register Expiration (minutes),
  `b+14` SUBSCRIBE for MWI, `b+16` Account Name, `b+23` Special Feature,
  `b+25` Voice Mail Access Number, `b+47` SIP Transport, `b+50` to `b+55`
  Preferred Vocoder choices 1 to 6 (`P451` to `P456` for account 2: the codec
  keys the earlier note had only from a template comment). Other keys sit
  100 (N - 1) above account 1's: `P2325` Feature Key Synchronization (account 2
  `P2425`), `P2347` Accept Incoming SIP from Proxy Only (`P2447`), `P26063` call
  waiting (`P26163`; the WP820 template has no such key, only the phone-wide
  `P91`), `P2397` OPTIONS Keep Alive (WP8x6 only, as for line 1) and `P2329`
  SIP URI Scheme when using TLS. Values are line 1's: Special Feature 102
  (Broadsoft, which the feature key sync needs), `P2347` = 1 so a second line
  is as closed as the first (S-32), call waiting 1 on / 2 off (WP856 2 on / 1
  off, as for line 1), transport 0 / 1 / 2 for UDP / TCP / TLS (the TCP value
  needs the SIP-over-TCP item). Not written for lines: SRTP (`P443`, `P543`,
  WP856 `P643`, `P1743`, `P1843`, with the SRTP item), DNS SRV and backup
  (`P408`, `P2412`, `P2433`, `P29195`, `P26140`, `P60156`, with the backup
  item: a phone in SRV mode still registers lines 2 and up by A record until
  those are added), RTCP-XR (`P26193`, `P60187`, `P26194`, `P26195`),
  PUBLISH/Subscribe for Registration (`P488`, `P2419`; both off by default) and
  send anonymous (`P421`). `TestMultiLineProvisioning` pins the keys for the WP8x6
  (two lines), WP822/825/820, and the WP856's six, and that a seventh and a
  third on a WP822 are not written. The deny list of the setting overrides
  covers the identity keys of accounts 2 to 6.
- **Account sharing (S-11).** `phone_groups.account_sharing` (the group switch)
  and `phones.sharing_role` host|guest (default host). Corrected against
  `wp8x6_config_1.0.3.39` ("Application/Account Sharing"): `P22521` is "Enable
  Account Sharing" (0 No, 1 Yes, default 0) and `P22522` is "Role in Account
  Sharing" (0 Host Device, 1 Guest Device, default 1), so they are not a
  host/guest pair of switches. The first version wrote host 1/0 and guest 0/1,
  which was right for a host by chance and switched sharing off for a guest.
  Now a WP8x6 in a group with the switch on gets `P22521 = 1` and `P22522` 0
  (host) or 1 (guest); any other WP8x6 gets `P22521 = 0`, so leaving the group
  turns sharing off. The WP810/822/825, WP820 and WP856 templates have no such
  key. Newly found, left at their defaults: `P22527` Group Name and `P22523`
  Group Password (both `AccountSharing`), `P22537` SIP Server Port (15060),
  `P22524` Account (0 Account 1, 1 Account 2, 2 Account 3, default 0) and
  `P22525` Account Name. Every Hello group therefore shares one group name and
  password; a per-group password would need a stored secret and is a follow-up.
  Rejected: a per-group host/guest value (a group of handsets needs a role per
  handset).
- **Key placement.** On a Yealink and a Snom, line key N is account N by default,
  so BLF and the voicemail key start after the lines (`PlaceBLFLines`). Fanvil's
  DSS keys are on the extended screen and Poly's attendant list is unbounded, so
  they do not shift. Line keys are not written explicitly (their types are not
  confirmed in the repo); the phones' defaults are relied on.
- **hello-sip.** Check-sync goes to line 1's contacts and falls back to the next
  line only while line 1 has none registered (`PushJob.Lines`), so a phone with
  two registered lines is notified once and one with line 1 down is still
  reached. Registration, MWI, ring tags and call handling are per device already.
- **Unchanged and open:** an incoming INVITE has the binding's contact as
  request URI and the dialled number in To (`b2bua.go`), so which line of a
  multi-line phone rings depends on the phone telling its accounts apart by the
  contact user it registered. To confirm on hardware.

## Provisioning parity items 15, 18, 29, 27 and the left-out vendor keys (2026-11-09)

### Item 15: setting overrides (S-12)

- Table `prov_setting_overrides` (migration 00069: numbered after the 00061 to
  00068 the open PRs use, so the PRs do not collide). One table for both scopes
  (`group_id` or `phone_id`), secret values sealed with the scope, owner and key
  as additional data.
- Applied to the finished per-phone file (`ApplyOverrides`), not through the
  template language, so an administrator's copy of a built-in gets them too.
  Only two formats are edited: Grandstream `<Pn>` elements and Yealink
  `key = value` lines. Poly (attributes under arbitrary element names), Snom
  (`idx`/`perm` attributes) and Fanvil (section-scoped `KEY :value`) are
  refused, because inserting a new key there would be a guess. They can be added
  once their insertion rules are confirmed on a phone.
- Deny list: the account, provisioning URL (carries the token), CA and admin
  password keys, from what the built-in templates write for them
  (`internal/prov/builtin/grandstream/device.xml`, `yealink/device.cfg`).
- Grandstream keys reachable through overrides, checked 2026-11-09 against
  Grandstream's own templates (config-template-10212026.zip): the numbers and
  values below replace the earlier "unconfirmed" note, which was written without
  the templates. Still no first-class fields (database, API, console) for side
  keys or speed dials: the overrides carry them, and building fields is a
  product decision rather than a missing key.
  - Speed dial, WP810/822/825 (`wp810_822_825_config_1.0.11.83`, section "P
    values not on WEB"): `P23001` account (0 Account 1, 1 Account 2) and
    `P23003` number (up to 64 characters, must match the dial plan) for key 0;
    keys 2 to 9 are `P23011`/`P23013`, `P23016`/`P23018`, `P23021`/`P23023`,
    `P23026`/`P23028`, `P23031`/`P23033`, `P23036`/`P23038`,
    `P23041`/`P23043`, `P23046`/`P23048`. The WP820 template lists the same
    numbers commented out (`wp820_config_1.0.7.90`, "Handset/Speed Dial") and
    adds the multi-function button: `P22258` function (0 PTT/Paging, 1 Mute, 2
    SAFE/Panic call, 3 Speed dial, 4 Disable), `P23051` account, `P23053`
    number. The same `P23001`/`P23003` in `wp856_config_1.0.3.16` are the
    account and user ID of expansion key 1 (`P23000` is its mode, 0 Speed Dial,
    1 BLF, 4 Speed Dial via active account, 5 DialDTMF, 8 Call Transfer, 9
    CallPark, 10 Call Intercom, 13 Multicast Paging, 15 Speed Conference, 16
    Dial prefix, 17 Send URL): a different feature, not the handset speed dial.
    The WP8x6 template has no speed dial key.
  - Off-hook auto dial `P71` (string, up to 512 characters) and its delay
    `P8388` (WP810/822/825: 0 to 30, default 4; WP820: 0 to 60): in those two
    templates and `ht813_config_1.0.19.6` (`P71`), not in the WP8x6 or WP856.
  - Wallpaper `P2916` (0 Default, 1 Download, 2 Uploaded) and `P2917` (server
    path): `wp810_822_825_config_1.0.11.83` and `wp820_config_1.0.7.90` only.
  - `P82281` VoWLAN Target Delay (0 low, 1 medium, 2 high, default 0):
    `wp8x6_config_1.0.3.39` and `wp810_822_825_config_1.0.11.83`.
  - `P82269` Wi-Fi power saving: the two templates disagree on the values, so
    the key is correct but the value is not portable. WP8x6: 0 Generic, 1
    Disabled, 2 U-APSD. WP810/822/825: 0 Optimum, 1 Relaxed Fast-PS, 2 Disable
    During Calling, 3 Disable. Not in the WP820 or WP856 templates.
  - `P25438` Wi-Fi Band (0 5G and 2.4G, 1 2.4G, 2 5G): `wp8x6_config_1.0.3.39`
    only. The WP820 uses two flags, `P25420` (2.4G) and `P25421` (5G), both 1 by
    default.
  - Side key `P82317`: `wp8x6_config_1.0.3.39` "Side Key Funtions", 0
    PTT/Paging, 2 Alarm, 4 None (default 0; the alarm value, 2, was unknown
    before). `wp856_config_1.0.3.16` calls it the PTT button with different
    values (0 PTT, 1 Mute, 3 Speed Dial, 4 Disable, 5 Scan, 6 Open the app) and
    adds `P82318` account (-1 dynamic, 0 to 5), `P82319` value and `P82320`
    description, and the left button `P82321` to `P82324` (default 5) and right
    button `P82325` to `P82328` (default 5). Hello writes `P82317 = 0` for a
    WP8x6 and a WP856 with PTT, which is PTT/Paging in both.
  - `TestOverrideWPKeys` renders each of these through the overrides. The
    deny list now also holds the identity keys of accounts 2 to 6 (active,
    server, user, auth ID, password, name, account name: `P401`, `P501`,
    `P601`, `P1701`, `P1801` and the offsets 0, 1, 3, 4, 5, 6, 16 in the WP8x6,
    WP810/822/825, WP820 and WP856 templates), which the lines of item 21 write.
    Speed dials replacing BLF on WP stay unbuilt as a feature.

### Item 18: vCard phonebook slot (S-31), partial

- Already on main: the model caps (WP822 500, WP8x6 1000) in `FormatForModel`,
  truncation of the tail of the delivery order, and P330 = 3, P331 without a
  scheme, P332 clamped to 5 to 720 (`BookRefreshFor`), P333 = 1.
- Built: `pb/0.vcf` (vCard 3.0, RFC 2426, folded at 75 octets) for the WP816,
  WP826 and WP836; `GET /api/v1/phones/{id}/directory` (delivered, capacity,
  dropped) and the warning on the phone page.
- Source for the vCard slot: the WP816/WP826/WP836 Administration Guide
  (documentation.grandstream.com/knowledge-base/wp8x6-administration-guide/,
  Phonebook: "Download vCard Phonebook", "Upload vCard Phonebook"). It names a
  web page upload only, so the slot is unused by the template (spec: "unused by
  default"). The vCard version the phone exports is not documented; 3.0 is the
  widest-read form and is **verify** on a phone.
- The two settings the Administration Guide names are in Grandstream's own
  configuration templates (config-template-10212026.zip, 2026-11-09 review), so
  they are written: `P333` "Remove Manually-edited Entries on Download" (0 No, 1
  Yes, default 1) is in `wp8x6_config_1.0.3.39` and
  `wp810_822_825_config_1.0.11.83` (confirmed; already written as 1);
  `P8462` "Import Group Method" (0 Replace, 1 Append, default 0) is in
  `wp8x6_config_1.0.3.39` only (newly found, written 0). `wp820_config_1.0.7.90`
  and `wp856_config_1.0.3.16` have neither; they have `P1435` "Clear The Old
  List" (0 No, 1 Clear all, 2 Keep Local Contacts, default 0) and `P1436`
  "Replace Duplicate Items" (WP820 default 0, WP856 default 1: 0 No, 1 by name,
  2 by number) in "Download Contacts". `P1435 = 1` is written for those two, the
  same effect as `P333 = 1`, and `P333` is no longer written to a WP820, which
  has no such key (corrected). `P1436` stays at its default.
- Corrected: `P332` on the WP820 and WP856 takes only 0, 5, 30, 60, 120, 240,
  360, 480 or 720 (both templates), not any value from 5 to 720; the WP820 now
  gets the WP856's rounding (`BookRefreshForPhone`). `P330` is 3 for HTTPS in
  every WP template, though the numbers 1 and 2 differ (WP8x6 1 HTTP, 2 TFTP;
  WP856 1 TFTP, 2 HTTP). The firmware gate "1.0.3.24 and later" is not checked:
  the page simply does not exist on older firmware.

### Item 29: backup and secondary servers (S-20), partial

- Sources for the Grandstream P-values: first read from the Pvalue-annotated
  copies of Grandstream's templates in the FusionPBX repository, then checked on
  2026-11-09 against Grandstream's own configuration templates
  (config-template-10212026.zip), which replace the third-party copies as the
  source. Result per key:
  - `P103` DNS Mode (0 A Record, 1 SRV, 2 NAPTR/SRV; the WP8x6 and WP810/822/825
    add 3 Use Configured IP) confirmed in `wp8x6_config_1.0.3.39`,
    `wp810_822_825_config_1.0.11.83`, `wp820_config_1.0.7.90`,
    `wp856_config_1.0.3.16` (and `ht813_config_1.0.19.6`).
  - `P29095` Register Before DNS SRV Fail-over (0 No, 1 Yes) confirmed in the
    same four.
  - `P26040` DNS SRV Failover Mode (0 Default, 1 saved one until DNS TTL, 2
    saved one until no response, 3 failback follows the failback expiration
    timer) confirmed in the same four, and now written `3` in SRV mode:
    without it the failback timer of `P60056` has nothing to time (added).
  - `P60056` Failback Timer (minutes, 1 to 64800, default 60) confirmed in the
    WP8x6, WP820 and WP856 templates. The WP810/822/825 template lists its
    "Failback Timer" under `P60055`, the key of "Maximum Number of SIP Request
    Retries" (default 4) a few lines above: a mistake in that template, so
    `P60056` is no longer written to the WP810/822/825 (corrected; it keeps its
    default of 60 minutes).
  - `P48` Outbound Proxy and `P2333` confirmed in all four ("Secondary Outbound
    Proxy" in the WP8x6, WP820 and WP856; "Backup Outbound Proxy" in the
    WP810/822/825). Newly found: `P2312` "Secondary SIP Server" in the WP8x6,
    WP810/822/825 and WP856 templates (not the WP820), the registrar-level
    counterpart of Yealink's `sip_server.2` and Poly's `server.2`; written with
    the backup next to the outbound proxy pair. Also newly found: `P2308` Primary
    IP, `P2309` Backup IP 1 and `P2310` Backup IP 2 (DNS mode 3, at most 15
    characters, so IPv4 without a port): they would give a second backup but
    cannot carry a port, so they are not used. Account 2 is `P402`, `P403`,
    `P2412`, `P2433`, `P408`, `P29195`, `P60156`, `P26140` in the WP8x6 (account
    3 `P502`...), left to item 21.
    Every one stays **verify** on a phone.
- Yealink `account.N.sip_server.2.address/port/transport_type/expires` and Poly
  `reg.N.server.2.address/port/transport/expires` are confirmed the same way
  (the T7x and VVX templates in the same repository) and are the same key
  families the Hello templates already use for server 1.
- Left out: Snom and Fanvil backup keys (snom `user_outbound` has an `idx` for
  the line, not a second registrar; no Fanvil key found), SRV mode for Yealink
  and Poly (their SRV setting is DNS-NAPTR, which is not plain SRV), the WP822's
  RN-only keys beyond the ones above, a second backup (Grandstream has one
  backup proxy slot and one secondary SIP server; P2308 to P2310 hold at most
  15-character IPs), and a chart
  that publishes DNS (Hello cannot manage DNS: the record set is documented).
- The backup list is an explicit setting, not read from an HA node list: on main
  hello-control has no list of the SIP nodes phones can use (the Kamailio pair
  PRs are not merged). The default stays A-record mode; `P103 = 0` is written to
  every Grandstream file so leaving SRV mode reverts.

### Item 27: provisioning hardening (S-22, S-32)

- TR-069 off is included: the user approved it explicitly on 2026-11-09 (the
  completion plan's open question). Grandstream WP: P1409 = 0 and an empty
  P4503; Yealink: `static.managementserver.enable = 0`.
- Keys and values, checked on 2026-11-09 against Grandstream's own configuration
  templates (config-template-10212026.zip: `wp8x6_config_1.0.3.39`,
  `wp810_822_825_config_1.0.11.83`, `wp820_config_1.0.7.90`,
  `wp856_config_1.0.3.16`), which replace the FusionPBX copies the first version
  was written from:
  - Confirmed in all four, value meaning as written: `P1409` Enable TR-069 (0
    No; the WP810/822/825 default is 1), `P4503` ACS URL (written empty; the
    WP8x6 and WP810/822/825 default is `https://acs.gdms.cloud`), `P8469` Enable
    User Web Access (0 Disabled), `P2347` Accept Incoming SIP from Proxy Only
    (1 Yes; the WP820 calls it "Only Accept SIP Requests from Known Servers"),
    `P26015` Allow SIP Reset (0 No). `P276` SSH: the templates ask the question
    both ways, "Enable SSH. 0 Yes, 1 No" in the WP8x6, WP820 and WP856 and
    "Disable SSH. 0 No, 1 Yes" in the WP810/822/825, so `1` is SSH off in all.
  - Corrected, written only where the template has the key: `P1357`
    Configuration via Keypad Menu is in the WP8x6, WP810/822/825 and WP820, not
    the WP856, and `1` is "Basic settings only" in all three (the WP810/822/825
    has `4` Basic and Network Settings, the WP820 `3` Basic settings and Network
    settings; either keeps Wi-Fi setup on the keypad, `1` stays per the spec and
    the Wi-Fi question stays a hardware check). `P1310` Direct IP Call is in the
    WP8x6 ("Enable Direct IP Call. 0 Yes, 1 No") and WP810/822/825 ("Disable
    Direct IP Call. 0 No, 1 Yes"), both off at `1`, and in neither the WP820 nor
    the WP856 template.
  - `P2347` is per account (account 2 `P2447`, account 3 `P2547`, WP856 accounts
    4 to 6 `P2647`, `P2747`, `P2847`): the lines of item 21 must write it too,
    or a second line accepts SIP from anywhere.
  - Newly found, added: the firmware upgrade confirmation prompt, which holds an
    upgrade until someone presses OK. `P8375` Firmware Upgrade Confirmation (0
    No, 1 Yes, default 1) in `wp8x6_config_1.0.3.39`, written 0; `P84988`
    Disable Firmware Upgrade Confirmation (0 No, 1 Yes, default 0) in
    `wp810_822_825_config_1.0.11.83`, written 1. The WP820 and WP856 templates
    have neither.
- Yealink TR-069 key: Yealink's own comment in the T-series templates,
  "Enable or disable the TR069 feature; 0-Disabled (default), 1-Enabled".
- Left out, each now with the meaning the templates give:
  - `P2346` Authenticate Incoming INVITE (0 No, 1 Yes, default 0; in all four
    templates, per account: account 2 `P2446`, account 3 `P2546`) and the S-22
    requirement that Hello answer a phone's 401 on an INVITE it originates
    (`TestAuthenticatedInviteToPhone`). With it on the phone challenges every
    INVITE it receives. sipgo's `WaitAnswer` answers a challenge only from a
    plaintext password and a device leg has HA1 only; a real phone challenges in
    its own realm (`<server host:port>`, see the resync fix), so the answer needs
    the sealed device password as `checksync.go` now uses. That is a change to
    the call setup path that cannot be validated without a phone. Not enabling
    `P2346` is what keeps calls working; the template test asserts it stays
    unwritten.
  - `P60082` Trusted Domain Name List (string up to 256 characters; WP8x6 and
    WP810/822/825 only) belongs to `P2311` Check Domain Certificates (RFC 5922,
    0 No, 1 Yes) in the same security block. The templates do not give the list
    format, and a wrong list would fail TLS registration, so neither is written.
  - `P240` Authenticate Conf File (0 No, 1 Yes; all four) and `P1359` XML Config
    File Password (string; all four): the HT813 template says a file "would be
    authenticated before acceptance" with `P240` on, but no template says how
    (Grandstream ships a config encryption tool with the bundle, and `P1359` is
    the password it uses), and Hello serves plain files over HTTPS with a token,
    so neither is written. `P1` is in none of the WP or HT813 templates.
  - Snom, Fanvil and Poly TR-069/SSH keys (Fanvil's text format key not found,
    Poly and Snom none found); outside this review.
- The group can undo any of it with a setting override (item 15).

### "Vendor template keys left out" (Poly caps/pickup, record keys, Fanvil CA, Snom/Yealink EXP, pickup values)

- Result: no new key. The tracker row names five gaps without the keys they
  need, and none could be confirmed this session:
  - Poly caps and pickup: the UC Software Administrator Guide is script-rendered
    and not fetchable (the same finding as `keycaps.go`); the FusionPBX Poly
    templates use `attendant.resourceList` up to 47 entries, which is a template
    default, not a documented limit. No BLF pickup attribute on a Poly key found.
  - Record keys: Yealink's `bw.call_recording.mode` and
    `account.N.call_recording.enable` are BroadSoft server-side recording, not a
    key that sends the `*1` toggle. No Poly equivalent found. The Grandstream
    record keys checked against Grandstream's own templates (2026-11-09):
    `P26028`/`P26029` (Call Recording On/Off codes) and `P29066` (Record Key
    Default Function, 0 Record On, 1 Record Off) are only in
    `wp8x6_config_1.0.3.39`, `P6760` (Record Mode, 0 Locally, 2 on UCM, 4
    Disabled) only in `wp856_config_1.0.3.16`: confirmed for those two families,
    absent from the WP810/822/825 and WP820 templates (nothing to write). Mode 2
    "on UCM" for the WP856 is a hardware check.
  - Fanvil CA: re-checked in the FusionPBX Fanvil templates (X-series, text and
    XML forms): no CA certificate key. Still none.
  - Yealink and Snom expansion modules: no Yealink Administrator Guide or Snom
    page beyond what `keycaps.go` already cites.
  - Pickup values: unchanged.
- Documented in `docs/provisioning.md` ("Keys deliberately not written") so the
  gaps are visible to operators. The tracker row stays open until sources are
  found; the setting overrides (item 15) can carry a key meanwhile.

## Item 26: PRACK/100rel and anonymous outbound (2026-11-09)

- 100rel is negotiated hop by hop, not relayed: Hello offers `Supported: 100rel`
  on every INVITE it originates and PRACKs any reliable 18x from a phone or
  trunk. Toward the caller it sends reliable provisionals only when the INVITE
  says `Require: 100rel`; `Supported` alone gets plain responses, as phones keep
  100rel off (P272 = 0). The 2xx waits up to 64*T1 for the PRACK. A PRACK that
  never arrives is traced and the call is answered anyway rather than failed.
- Anonymous outbound is trunk-leg only. It triggers on the trunk policy
  `always`, the extension flag, or a device request (Privacy id/user/header, or
  From `anonymous`; `Privacy: none` cancels). The leg gets an anonymous From,
  the trunk's asserted identity in PAI or PPI (trunk setting, default PAI) and
  `Privacy: id` merged with the device's values. A device's own PAI/PPI is
  never forwarded.
- The spec has no feature code for per-call anonymity, so none was added: a
  per-call request is the device's Privacy header or anonymous From.
- The WP "Send Anonymous" setting is found in Grandstream's own templates
  (config-template-10212026.zip, 2026-11-09 review): `P65` "Send Anonymous
  (caller ID will be blocked if set to Yes). 0 - No, 1 - Yes. Default is 0" for
  account 1, in `wp8x6_config_1.0.3.39`, `wp810_822_825_config_1.0.11.83`,
  `wp820_config_1.0.7.90` and `wp856_config_1.0.3.16` (also
  `ht813_config_1.0.19.6`); account 2 is `P421`, account 3 `P521` (WP8x6,
  WP856). It is deliberately not written: the extension flag
  `anonymousOutbound` is applied by Hello on trunk legs only, so a phone sending
  an anonymous From on every call, internal ones included, would add nothing the
  flag does not already do and would blank the caller on internal calls. A site
  that wants one phone always anonymous sets `P65 = 1` through the S-12 per-phone
  override. `TestAnonymousKeyNotProvisioned` pins this. Related keys in the same
  templates, left at their defaults: `P2338` Use Privacy Header (0 Default, 1
  No, 2 Yes), `P2339` Use P-Preferred-Identity Header (0 Default, 1 No, 2 Yes),
  `P29098` Use P-Asserted-Identity Header (0 No, 1 Yes; WP8x6 and WP856
  templates only), `P129` Anonymous Call Rejection (0 No, 1 Yes; Hello rejects
  anonymous callers itself, item A1) and `P272` Enable 100rel (0 No, 1 Yes,
  default 0, confirmed as the key behind "phones keep 100rel off").
- Migration 00051: `extensions.anonymous_outbound`, `trunks.anonymous_policy`,
  `trunks.asserted_identity`, `trunks.identity_header`. Replaces and call
  waiting of the same delivery-plan item are not in this PR.

## RTCP-XR collector (parity-rtcpxr, item 35, S-36) (2026-11-09)

- Reports live in their own table `cdr_rtcpxr` (migration 00066), not in
  columns on `cdrs` as the spec's Data says. A phone sends its call-end
  report within moments of the BYE, usually before the CDR is written, and
  the report names the phone's own leg of a B2BUA call, which is not always
  the CDR's `sip_call_id`. The CDR therefore also stores the answering callee
  leg's Call-ID (`cdrs.peer_call_id`) and `GetCDR` joins on either. A report
  with no CDR yet (or ever) costs nothing.
- One row per (dialog Call-ID, device): a later report replaces the earlier,
  an interval report never replaces the call-end one. Kept 90 days, pruned on
  every 1000th write. A device is held to 60 reports a minute in memory per
  node. Remote metrics (what the far end reported) are ignored; the phone's
  own receive-side view is the figure.
- A malformed or out-of-range report (MOS outside 0 to 5, loss outside 0 to
  100, no dialog id, no metrics, over 4 KiB) is answered 400 and stored not at
  all. Wrong content type 415, other Event 489 (item 34 adds presence),
  store down 503. The sender must authenticate as a device.
- Vendor keys, checked against Grandstream's own configuration templates
  (config-template-10212026.zip; 2026-11-09 review). Account 1:
  P26093 collector name, P26094 collector address and P26095 collector port
  (default 5060) are in `wp8x6_config_1.0.3.39`,
  `wp810_822_825_config_1.0.11.83` and `wp856_config_1.0.3.16` (confirmed;
  account 2 is P26193, P26194, P26195 in the same three). P60087 collector
  address selection (0 Manual, 1 Auto) is in `wp8x6_config_1.0.3.39` and
  `wp810_822_825_config_1.0.11.83` only (confirmed; account 2 is P60187).
  P8489 "VQ RTCP-XR Session Report" (0 Disabled, 1 Enabled, default 0) is in
  the same two and is newly found: without it the phone sends no call-end
  report, so the collector would stay empty. `wp820_config_1.0.7.90` has no
  RTCP-XR key. The earlier reading that the WP8x6 and WP856 name no such keys
  (from release notes) was wrong: corrected, they now get the keys.
  Template (only when `HELLO_PROV_RTCPXR=true`, default off, spec "off by
  default"): WP8x6 and WP810/822/825 get P26093 = SIP server host, P60087 = 1,
  empty P26094, P26095 = SIP server port, P8489 = 1; the WP856 has no Auto
  selection and no session report key, so it gets P26093 and P26094 = SIP
  server host and P26095, and nothing else; the WP820 and non-WP Grandstream
  phones get none. P8490 (interval report) stays at its default off, because
  the collector keeps the call-end report. Yealink, Poly, Snom and Fanvil are
  not provisioned (no confirmed keys read). Behaviour is not yet seen on a
  phone (verify: Auto with an empty address; the WP856 with the address set).
- No MCP tool added; nothing to teach in skills/. The CDR REST read gains
  `rtcpxr`, and the call detail page shows it beside the relay figures.
- Merge note: `handlePublish` in `internal/sip/rtcpxr.go` owns the PUBLISH
  route and answers 489 for other events; item 34 (presence PUBLISH) should
  dispatch its event from there.

## SIP over TCP is provisioned per model, on the UDP address (2026-11-09)

Phone-feature-parity S-13 (delivery item 22). hello-sip already records the
phone leg's transport (`X-Hello-Transport`, set by Kamailio) on the binding, and
Kamailio already shares port 5060 between UDP and TCP, reuses the phone's
connection for requests towards it (`phones_tcp` socket, alias with the
protocol) and closes idle connections after an hour. What was missing was
provisioning. `HELLO_PROV_SIP_TCP_MODELS` (same format as the TLS list, default
none, so nothing changes until a model is listed) puts the listed models on
transport TCP with `HELLO_PROV_SIP_SERVER`: Grandstream `P130 = 1`, Yealink
`transport_type = 1`, Poly `TCPOnly`, Fanvil `SIP1 Transport = 1`; Snom has no
confirmed key and stays UDP. A model in both lists uses TLS while the TLS server
is set, and falls back to TCP when it is not. The setting is per model, not per
device: a device that needs another transport has its model taken out of the
list (per-device overrides belong to S-12). Helm: `control.prov.sipTcpModels`.
Keys checked against Grandstream's own configuration templates
(config-template-10212026.zip, 2026-11-09 review): `P130` "SIP Transport. 0 -
UDP, 1 - TCP, 2 - TLS/TCP" is the same key with the same values in
`wp8x6_config_1.0.3.39`, `wp810_822_825_config_1.0.11.83`,
`wp820_config_1.0.7.90`, `wp856_config_1.0.3.16` and `ht813_config_1.0.19.6`
(confirmed). Account 2 is `P448` and account 3 `P548` in the WP8x6 (account 2 in
the WP810/822/825 and WP820, accounts 2 to 6 in the WP856 at `P448`, `P548`,
`P648`, `P1748`, `P1848`); lines 2 and up are item 21. Newly found and left at
their defaults: `P2331` Use Actual Ephemeral Port in Contact with TCP/TLS
(default 0) and `P60065` Enable TCP Keep-alive (default 1), in the same
templates. The other vendors' keys remain from vendor notes, unconfirmed on
hardware.

## Presence extensions (item 34, S-35)

- Resource list URI: `sip:blf-<device username>@<domain>`. Its members are the BLF keys of the phone bound to that device (`phones.blf`), loaded into the snapshot (`WithBLFLists`); only that device may subscribe (`403` otherwise), and the phone must send `Supported: eventlist` (`421` with `Require: eventlist`). Editing a phone's BLF keys now bumps the configuration revision so the list reaches hello-sip. No migration.
- Datagram limit: sipgo refuses UDP packets over 1300 bytes, so a list is one full-state NOTIFY plus partial ones (RFC 4662 `fullState="false"`), and a change sends only the changed resource. At about 600 body bytes a resource, that is one resource per NOTIFY. TCP/TLS toward Kamailio (#258) would allow bigger chunks; the budget constant stays until then.
- PUBLISH event state lives in Valkey (`hello:pubpres:<ext>:<etag>`, the publication's lifetime as TTL), so both hello-sip nodes serve, refresh and remove it. Nodes learn of the other node's PUBLISH and REGISTER by a 5 s poll of the shared state (`presencePollInterval`), plus an immediate NOTIFY for changes handled locally. Kamailio needs no change: PUBLISH and SUBSCRIBE already go to any Hello node.
- Several publications per extension are allowed (a desk phone and a softphone); the extension is "open" if any is open, the newest note wins. With none, it is "open" while a device is registered.
- Grandstream keys, checked against Grandstream's own configuration templates
  (config-template-10212026.zip, 2026-11-09 review). `P188` "PUBLISH for
  Presence" (0 Disabled, 1 Enabled, default 0; account 2 `P488`, account 3
  `P588` in the WP8x6) and `P2319` "SUBSCRIBE for Registration" (0 No, 1 Yes,
  default 0; account 2 `P2419`, account 3 `P2519`) are in `wp8x6_config_1.0.3.39`
  and `wp810_822_825_config_1.0.11.83` (confirmed), and in neither
  `wp820_config_1.0.7.90` nor `wp856_config_1.0.3.16`. The spec keeps both off for
  the WP phones [R8x6 section 4], so the template now writes `P188 = 0` and
  `P2319 = 0` for the WP8x6 and WP810/822/825 (a phone someone switched on goes
  back); a test asserts the rendered keys and that the WP820 and WP856, whose
  templates lack them, get none. No WP template has an Eventlist BLF URI, a
  Presence Eventlist URI or a PUBLISH-for-presence list key: not in the
  templates, so `.Line.BLFListURI` is not written to a WP. The key exists only in
  the GRP templates (`grp26xx_config_1.0.15.23`: `P134` Eventlist BLF URI,
  `P26084` Presence Eventlist URI, `P2389` Auto Provision Eventlists, 0
  Disabled, 1 BLF Eventlist, 2 Presence Eventlist; account 2 `P444`, `P26184`,
  `P2489`); Hello's non-WP Grandstream BLF block writes per-key subscriptions
  (`P323` and on), so switching to the list is a hardware decision and is not
  written. Yealink, Snom, Fanvil and Poly keys were not in scope and stay unwritten.
- The spec lists no UI or REST fields for S-35, so none were added.
- PUBLISH for events other than presence answers `489`; `vq-rtcpxr` (S-36, item 35) is not served here.

## Kamailio HA: the VIP ships off, go-live is a separate step (2026-11-09)

The user's deploy constraint: the PRs of kamailio-ha get merged ready, but the
change that re-points phones to the VIP must not go live until the user asks.
Sync deploys `deploy/kuvryn-sync/kw/resources.yaml` as soon as it is on main,
so the VIP stage cannot sit in that file.

- `kw/resources.yaml` keeps the state of PR 1/4 (a pair on their own node
  addresses; phones stay on `192.168.10.101:30508`). The config with
  `EDGE_VIP`/`LEGACY_IP` support ships in the ConfigMap (inert: neither is set),
  which rolls the Kamailio pair once (new config checksum).
- Everything that turns the VIP on is `deploy/kuvryn-sync/kamailio-vip/go-live.patch`
  (pod env, the kube-vip container, its ServiceAccount, Role and RoleBinding,
  and the control plane's advertised proxy host). `test/deploy` and the
  `test/kindha` failover lab apply it to a copy, so the patch is always
  proven and always applies; `TestKwKamailioVIPIsOff` guards the off state.
- Alternatives: a values flag cannot work for kw (plain manifests, no
  templating); keeping the PRs unmerged blocks the chart and docs for other
  clusters. The chart option (4/4) defaults to off.
- Merge conflict note: #201 was squash-merged, so the stacked branches
  re-added its files; resolved by taking the stack's version of the VIP and
  legacy-socket code and main's TLS socket code (the VIP branch of the
  listens also gets a TLS listener on `EDGE_VIP`).

## TestConferenceTwoNodes flake: root cause (2026-11-09)

- Two causes, one in the test and one in production code. Both reproduced on a
  Mac (16 parallel test processes, or 12 busy loops).
- "rehomed = 0, want 1" (also seen in CI, run 40695787431): the test read the
  counter as soon as `conf.get("800")` was non-nil. `confRehome` makes the room
  visible in `confReserve` and counts `rehomed` several statements later, after
  `NewParty`, so a goroutine switch in between shows 0. The test now waits for
  the counter. The production order stays: the event is counted only once the
  rehome has succeeded.
- "forwarded participant hears no audio" (peak 0): the relay and anchor sockets
  were bound with network `udp` (dual-stack `[::]`), the conference party and
  fanout sockets with `udp4`. On BSD kernels a dual-stack bind succeeds on a
  port an IPv4 socket already holds, and IPv4 datagrams then go to the IPv4
  socket. A relay leg could share a port with a party socket (a previous run's,
  or another test process's), and the phone's media never reached the relay
  (relay saw 0 packets, 0 rejected). Linux refuses that bind, so the next free
  port is taken. All RTP is IPv4-only (SDP is `IN IP4` only), so relay and
  anchor sockets are now `udp4` like the rest; the port-in-use check is then
  the same on every kernel.

## Opus "inLen >= S->Fs_in_kHz" flake: root cause and fix (2026-11-09)

- Symptom: `TestOpusSessionLoopback` failed on most runs (104 of 200 here, less
  on CI) with the SILK resampler assertion; the same assertion hit a voicemail
  call in production. PR 243's rebuild-after-panic only recovers; it did not
  remove the cause.
- Root cause: the transpiled libopus passes addresses of its Go stack locals
  as uintptr. `silk_decode_frame` writes the frame length through one; if the
  stack moves during the call the write goes to the old copy, the caller reads
  0, and `silk_resampler` aborts. The pin in `withOpusStack` was meant to
  prevent the move, but `var pin [512<<10]byte` is over the compiler's 128 KB
  limit for stack variables, so it was moved to the heap (`go build -gcflags=-m`
  says "moved to heap: pin"). It pinned nothing and allocated 512 KB per call.
  Survival depended on the caller's stack at that moment (a fresh goroutine, or
  a receive loop a GC had shrunk while parked in a read).
- Ruled out: shared decoder state across goroutines (each decoder has its own
  TLS and mutex; sessions use one goroutine per decoder), frame sizes (320
  samples, packets replay clean on a big stack), test timing.
- Fix: all library calls run on a few dedicated worker goroutines that build a
  512 KB stack once from 64 KB frames (under the 128 KB limit) and hold it, so
  nothing grows or shrinks it. A call that still moves the stack is returned as
  a failure, which feeds the existing decoder rebuild. The test expectation was
  right and is unchanged.
- Under `-race` the Opus build is off (opus_race.go), so the loopback test
  skips there; the -race count proves nothing about the codec.

## IVR homed-callee data race (ivrHomeCallee vs release)

- Root cause: `homedCall.legs` had no lock of its own. `ivrHomeCallee`
  (IVR dial-out goroutine, started by takeover) wrote `legs[1]` under
  `call.mu`, while `call.release` (BYE handler goroutine) iterated `hom.legs`
  after dropping `call.mu`; `haHomedState` and `homedCall.leg` read it with no
  lock either. Seen in `TestIVRHuntAnswerAtMemberTimeout` under `-race`.
- Fix: `homedCall` carries a leaf mutex; all access goes through `leg`,
  `pair` and `setCallee`. Regression test: `homed_race_test.go`.

## Grandstream key choices from the official templates (2026-11-09)

- Firmware-upgrade confirmation prompt off (P8375=0, P84988=1) for unattended upgrades, or leave it
- Send Anonymous (P65): stay unwritten (extension flag anonymises trunk legs) or write it
- Account sharing group password: shared default `AccountSharing`, or a per-group secret

## Flow canvas P7: node badges and extensible kinds (2026-11-09)

- **What was already done.** The graph side of P7, the batch ops for every
  merged object (`TestBatchCoverage`), time conditions with override in the
  trace (P5) and the catalogue guards were on main. The one visible gap was the
  badges: the server sent `summary.forced` / `summary.generated` but the canvas
  drew them as plain text.
- **Badges are rules, not per-kind code.** `web/src/pages/flow/badges.ts` holds
  a list of rules `(node, summary) -> badge`; `buildView` puts the result in
  `FlowNodeData.badges` and `FlowNodeView` draws it. Override badge: a time
  condition with `forced` shows "Override: open|closed" (tooltip says the
  schedule is ignored). Generated badge: a TTS announcement shows "Generated".
  A new kind (follow-me, fax, locations) adds one rule and no other web code;
  unknown kinds already render from the catalogue.
- **Open PRs (#272, #273, #275)** add their kinds through the checklist in
  `internal/flow/README.md`; nothing here depends on them. No API, migration or
  MCP change, so no conformance, count or skill update is needed.
## AI on the canvas may apply its own batch (2026-11-10)

- Asked: should the canvas AI assistant only propose (a person applies), or may it apply?
- Answer (user): the AI may apply. Give the assistant a write tool that applies its batch,
  audited and revertible, in line with the earlier AI-access decision. Operators still see
  and can revert every applied batch.

## Conference two-node test raced on srv.deps (2026-11-10)

- Root cause: not production code. `TestConferenceTwoNodes` assigned
  `p.srv.deps.Membership` after `startPBX` had already started the servers, so
  the test goroutine wrote `s.deps` while sipgo handler goroutines (`confMember`
  via `confResolve`) and `confWatchPeer` read it. Seen under `-race` in the
  integration job. The server never writes `deps` after construction.
- Fix: inject the membership through a `withMembership` pbxOpt at build time,
  as `withHA` and `withConferences` do. Rule: tests never mutate `srv.deps`
  once the node runs.

## TestValkeyFailover red in the nightly lab since 2026-11-07

- Root cause: a lab/runner problem, not a Hello bug. The first failing run is
  the first one on the GitHub-hosted `ubuntu-24.04-arm` runner (#249); the last
  green ran on ARC (`arc-azrtydxb`). After `docker compose stop valkey-1` all
  three Sentinels go sdown on the primary within 5 s and then sit in TILT
  (`+tilt` every ~8 s, never `-tilt`), which suspends failover; even a manual
  `SENTINEL failover` answered NOGOODSLAVE. Hello never gets to reconnect, so
  nothing in hello-sip/hello-control or Kamailio HA (#201 landed after the first
  failure) is involved. The later failures in that run cascade from the
  still-stopped primary.
- Mechanism (confirmed on the hosted runner with probes in the lab): Sentinel
  resolves `valkey-1` synchronously (resolve-hostnames). A stopped container's
  name is unknown to Docker's embedded DNS; musl then adds the runner's search
  domain (`*.cloudapp.net`) and the query is forwarded upstream and never
  answered, so `getent hosts valkey-1` blocks 10 s. That stalls the Sentinel
  loop past the 2 s tilt trigger, over and over. Measured in the same
  container: default 10 s, `--dns 127.0.0.1` 10 s, an internal network 10 s,
  `timeout:1 attempts:1` 2 s, `ndots:0` 0 s.
- Fix: `dns_opt: ["ndots:0"]` on the lab Sentinels, so the bare service name
  gets NXDOMAIN at once. (Making the `state` network internal was tried and did
  not help.)
- kw is not affected: sentinels and valkey are healthy there (ckquorum OK 3
  usable, primary valkey-1) and in-cluster DNS answers a missing pod fast.

## Relay hook data race (2026-11-10)

- Root cause: `Relay.recv` tested `r.onPacket` / `r.onDTMF` for nil without `r.mu`, while
  `OnPacket` / `OnDTMF` set them under it. Seen in `TestQueueCallbackReservesAgent` under `-race`.
- Fix: the receive loop reads each hook only under the lock, as #266 and #273 already do, so
  they rebase onto this cleanly. Regression test: `relay_hook_race_test.go`.

## Kamailio HA flake: "graceful loss of the active instance" (2026-11-10)

- Root cause: a real handover race, not kind timing. On a graceful delete the
  vip container releases the Lease at once, but the replacement pod is created
  immediately and scheduled the moment the old pod frees the node's ports
  (hostNetwork), about 1 s later. Its kube-vip starts electing while its
  Kamailio is still starting (the startup/liveness probes do not gate the
  election). It races the survivor, whose acquire loop retries every 1 s
  (jitter up to 2.2 s), and sometimes wins: the VIP lands on the freshly
  started, not yet serving instance and stays there, since it is healthy.
- Evidence (failed runs 42784203986 and 42782832256, both TestKamailioHAChart):
  after the graceful subtest killed the holder on node X, the next subtest's
  holder was X again (the replacement pod); the survivor never held the VIP in
  the 60 s the test waits ("no single holder ... skipping [X]"). In passing runs
  the survivor took it in 0.6-2.3 s. Graceful restarts of the active pod show
  "VIP dark at most 3.4 s" in the rolling restart for the same reason.
- Fix (production first): `lifecycle.preStop: sleep 5` on the kamailio container
  (chart: `kamailio.vip.handoffSeconds`, default 5, only with the VIP; same in
  deploy/kuvryn-sync/kamailio-vip/go-live.patch). The old pod keeps the node's
  ports for 5 s after SIGTERM, so the replacement cannot start before the
  survivor holds the Lease. Regression test: TestKamailioHandoffPreStop.
- kw: not affected today (the VIP is not live, kamailio pods have no vip
  sidecar), but go-live would be, so the go-live patch carries the preStop.
  deploy/kuvryn-sync/kw/resources.yaml itself is untouched; it needs the same
  preStop only if the VIP is wired into it directly.

## Backup tools run from PATH only (2026-11-10)

- `pg_dump` and `pg_restore` are run by their names from PATH. The
  `HELLO_BACKUP_PG_DUMP` and `HELLO_BACKUP_PG_RESTORE` overrides are removed
  (config, README, docs/backup.md).
- Why: the semgrep `dangerous-exec-command` gate blocks any exec of a
  non-static command, and no supported allow exists for it. The overrides were
  unused: kw does not set them (checked by the user).
