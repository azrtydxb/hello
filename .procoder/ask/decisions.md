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

## Messaging inbox core (2026-10-26)

Delivery item 1 of `.procoder/specs/messaging-channels.md`, built LAN-only on the
SIP text messaging of PR #165. Migration 00046 `messaging_core`; scope `messages`
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
