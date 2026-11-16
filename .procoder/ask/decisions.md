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

## Door-open DTMF `*50` (2026-11-09) — phone-feature-parity item 14 (S-47)

- Migration `00078_door_open` (after #225's 00053; the number is a gap-numbered
  slot like the others, goose does not need it contiguous): table `door_open`
  (`extension_id` primary key, cascade; `code_enc` BYTEA sealed under
  HELLO_SECRET_KEY with AAD `door-open:<extension id>`; `hangup_after_open`).
  A separate table, not columns on `extensions`, so none of the extension
  queries scan or return the code. A station that becomes a user loses its row.
- The code rides the configuration snapshot (opened at load, unexported and
  redacting its `String`), so the SIP path reads no PostgreSQL. A code that does
  not open is logged by extension number only and treated as no code.
- Settings API: `GET` (viewer) says `codeSet` and `hangupAfterOpen`; `PUT` is
  admin (scope `admin`, role admin). It could not use the `secrets` scope: that
  list is pinned by spec S-5 and `TestRoutesTable`. The code is write-only, an
  empty string clears it, a user extension answers `409`. `GET` is an MCP tool
  (`getDoorOpen`, taught in hello-setup); `PUT` and the open request are
  excluded from MCP (a code must be set in the console; opening a physical door
  is a user action).
- `*50` is matched as the end of the call's digit buffer, in `dispatchDigit`
  after the feature codes, so a stale digit before it does not hide it; a
  feature code row named `*50` wins. It acts only on a connected call with a door
  station, only for keys from the far side of the station (the station's own
  keys never open its door), and otherwise does nothing: the keys were already
  relayed as ordinary DTMF.
- The code goes out through `Relay.SendDTMF`: 5 packets of 20 ms per digit and 3
  end packets, a 60 ms gap between digits, as its own RTP stream (fresh SSRC)
  like `PlayTo`, using the leg's negotiated telephone-event type. Without one the
  trace says so and nothing is sent. Door calls are always anchored today (every
  call is), which `*50` needs. Open: whether GDS/2N/Akuvox accept events on a
  different SSRC than the call's audio; hardware check.
- The user's own `*`, `5`, `0` events still reach the door before the code
  (the relay forwards DTMF); suppressing them would need the relay to hold back
  events, which is not done.
- Only one open at a time per call (`doorOpening`); a second press while the
  first is going out is refused with a trace line.
- API open: `POST /api/v1/calls/{id}/door-open` (user role, write scope; added
  to the self-service allowlist of `TestRoutesTable`, the spec fixes the path).
  The API checks the caller's extension is the one that answered (live call
  `callee`), or placed the call to a station, queues a park-command
  `door_open` to the call's node and waits for the reply (the existing
  Valkey park command channel); the node checks again against the live dialog.
  `403` has error code `not_your_call`, `404` no such call, `409` not an answered
  door call, no code, or media unusable, `502` node error, `504` no node
  answered. `GET /api/v1/me/door-calls` lists the user's own for the console;
  live calls gained `door` (the station's number).
- Hang-up-after-open: `HELLO_DOOR_HANGUP_SECONDS` (default 3, positive) after
  the code finished sending, both legs get BYE (`endBoth`, CDR side system).
- Not done: provisioning an "Open door" key on the phones (Yealink DSS DTMF,
  Grandstream WP DialDTMF softkey, Poly/Snom/Fanvil soft keys). The spec marks
  each **verify**, and no vendor key was confirmed from the templates in this
  change; it stays with the hardware pass.

## Door video preview (2026-11-10) — phone-feature-parity item 17 (S-49)

- Three parts, all optional like the spec says: video in the call, a camera
  picture in the console, and the Grandstream WP door-system entries.
- Migration `00086_door_cameras` (after item 14's 00085): `door_cameras` keyed by the station, kind mjpeg or snapshot, URL, user
  name, password sealed with AAD `door-camera:<extension id>`.
- **Video in the call.** Only when a door station is a party and the caller's
  SDP offers H.264 over plain RTP/AVP(F). The anchor adds a `VideoRelay`: a
  second RTP `Relay` on an even/odd port pair per leg plus an RTCP bridge on the
  odd ports, so a phone's PLI/FIR reaches the door. RTCP is accepted only from
  the sources the RTP leg accepts. Each SDP gets one video m-line on Hello's
  ports with the H.264 payload types, fmtp and feedback lines of the other side,
  and an explicit `a=rtcp`; the callee gets the caller's section, the caller
  the callee's. A callee with no video gets none and the door's offer is
  answered with port 0 (RFC 3264). A call with no door station is untouched
  (video beyond S-49 is out of scope, as the spec says). Re-INVITEs on an
  anchored call keep the section on both sides; hold does not touch the video
  direction. Not carried: takeover (HA), transfer, re-anchor, SRTP, VP8. A phone
  that sends RTCP on the RTP port (rtcp-mux) is not supported; the section does
  not offer it.
- **Camera in the console.** Hello fetches the picture itself and the browser
  gets it from `GET /extensions/{id}/camera/view` (viewer), so neither the
  browser nor a user needs a route to the camera or its credentials. Settings
  are admin (`PUT`, `DELETE`); `GET` (viewer) never returns the password. MJPEG
  is passed through (10 minutes at most, eight pictures at once per node, `429`
  above), a snapshot is fetched per request (10 s, 4 MiB). The console polls a
  snapshot every 2 s. `rtsp://` is refused with a field error: a browser cannot
  show it and transcoding H.264 to MJPEG is out of scope. Basic authentication
  only (no digest). No redirects are followed.
- SSRF: the client is `safehttp.NewTransport`, so loopback, link-local and
  metadata addresses are never reachable and private networks only when listed
  in the new `HELLO_DOOR_CAMERA_ALLOW` (hello-control, CIDRs, validated by
  `safehttp.ParseAllow`; the error names the key and not the value). Empty by
  default: a camera on a LAN needs its network listed. kw's values are not set
  here (deploy/kuvryn-sync is out of scope); the parent task must add the camera
  network there.
- The phone's own copy of the stream is the first part (SDP video); no camera
  URL is provisioned to phones, and there is no phone-side RTSP.
- **WP door systems.** The WP8x6 (`wp8x6_config_1.0.3.39`, Settings/External
  Service/Door System) and WP810/820/822/825 (`wp810_822_825_config_1.0.11.83`,
  Settings/External Service) take ten entries of five P-values from P32000: type
  (0 GDS), account (0), system identification (name), system number (the
  station's number), access password. Hello writes the first four per station
  that can call the phone's extension (target, listed group, or a Door group it
  belongs to). It never writes the access password: the sealed door-open code
  stays on the server and `*50` (item 14) opens the door. The WP856 uses
  P32001 for another meaning and gets none; other vendors get none. The key
  meaning is from the official templates; what a WP does with an entry that has
  no access password is a bench check.
- Open-door softkeys (item 14's "Open door" key) are not provisioned: the
  WP8x6 key P-values for DialDTMF/GDS Opendoor are listed in the templates but
  need a key-placement design and a WP to try; left for the bench.

## Doorbell webhook (2026-11-09) — phone-feature-parity item 16 (S-48)

- Independent of #222 (outbound event webhooks: signing, retries, dead
  letters). S-48 is the inverse: an inbound, signed trigger. Nothing is shared
  with `internal/webhook`; the routes live under `/api/v1/door/webhooks` and the
  table is `door_webhooks`, so the two cannot collide.
- Migration `00078_door_webhooks`. #225 owns 00053 and the item-14 PR 00054; the
  next free numbers of the open PRs are 00061 and 00062 (00055 to 00060 and 00063
  to 00066 are taken), so 00061 is this one and 00062 is item 17. Columns: name,
  sealed secret (AAD `door-webhook:<id>`), action, ring group (null: every Door
  group), paging group, announcement (set null when it is deleted: the built-in
  chime), rate limit, enabled, last triggered. Check constraints keep a ring
  action from naming a paging group and a chime from missing one.
- Management is admin (scope admin; read for list and get, viewer). Not the
  `secrets` scope, which spec S-5 and `TestRoutesTable` pin to a fixed list.
  Create and rotate return the secret once; no other response has it. All
  management routes are excluded from MCP (a signing secret is a console
  matter), so no tool needs teaching in `skills/`.
- Trigger route: the only new public route (added to the public lists of
  `TestRoutesTable` and `TestVersionAndOpenAPI`). Order of checks: both headers
  present, timestamp an integer within 300 s, body at most 1 KiB (`413`), then
  the signature, computed for an unknown id against a random per-process decoy
  secret so an unknown, a disabled and a wrong-secret request cost the same and
  give the same `401`. Only after the signature: body must be a JSON object
  (`400`), Valkey present (`503`), rate limit (`429` with `Retry-After`), duplicate
  window, enqueue. A disabled webhook is `401`, not `403`, for the same reason.
- Rate limit: a fixed one-minute window per webhook in Valkey, counted by a Lua
  script that sets the expiry in the same step as the first count (a crash
  between INCR and EXPIRE cannot lock a webhook out); default 11, 1 to 600 per
  webhook. It counts every signed request, coalesced ones included.
- Coalescing: a `SET NX PX` of 10 s per webhook. A second signed trigger inside
  it answers `202 {"status":"coalesced"}` and queues nothing. Chosen over
  de-duplicating on the label because a doorbell button has no idempotency key.
- Origination reuses the paging-group test path instead of a synthetic `call`:
  hello-control queues a `DoorTrigger` (Valkey list, 60 s TTL), any READY
  hello-sip node takes it and sends INVITEs to the registered contacts of the
  members (like `testPage`), with a sendonly offer from a media fan-out. The
  ring carries the doorbell Alert-Info from the ring tag table and the webhook's
  name as caller name (From `door-webhook@<domain>`); the first answer wins and
  the rest are CANCELled; the winner hears the chime (announcement, or a built-in
  two-note chime), is hung up, and the call also ends after 30 s.
- Group strategy is not honoured by the webhook: every member rings at once (the
  strategies belong to calls that have a caller to hold). A member in DND (the
  group's ignore-DND aside), in a call, or a door station is skipped and the
  trace says so; the longest ring timeout of the groups bounds the ringing.
- `chime_page` uses the page auto-answer headers and PAI of a server page and
  plays to every member that answers.
- CDR: kind door, source the webhook name, destination the group (or code),
  ring tag doorbell for a ring, status 200 when somebody answered, 480 when
  nobody could be reached. Audit rows are `webhook:<name>` `trigger` or
  `coalesce`, without the body or label. The label (cut to 64 printable
  characters) appears only in the call trace.
- Console: a Door page under Call flow (list, create, edit, new secret, delete;
  viewers see no write control), the secret in a notice that goes away for good
  when dismissed.
- Not done: the trigger is acknowledged when queued, not when someone answered,
  so the doorbell cannot tell. Trigger results could be polled later if a
  doorbell needs it.

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

## SRTP SDES on the anchor (phone-feature-parity item 24, S-15) (2026-11-09)

Built in `internal/media/srtp.go` (keys, SDES, protect/unprotect on the relay
legs) and `internal/sip/srtp.go` (policy and negotiation). Choices:

- **Library: `github.com/pion/srtp/v3` v3.0.10.** Maintained, pure Go (keeps
  `CGO_ENABLED=0`), same Pion family as the decided anchor modules. It brings
  `pion/rtp`, `pion/rtcp`, `pion/transport` and `pion/logging` as indirect
  modules; the spec's "one new module" limit was amended for S-16 and this is
  the same family. Measured on a laptop: protect plus unprotect of a 160-byte
  packet is about 0.75 µs, far under the 50 µs p99 bound (`TestSRTPPacketP99`).
- **Per-leg keys.** A relay leg has a local key (Hello sends with it, written
  in the SDP for that leg) and a remote key (the far end sends with it). The
  relay decrypts with the receiving leg's remote key, rewrites as plain RTP,
  and encrypts with the sending leg's local key, so the legs never share a key
  and the recording tap, DTMF and announcements see clear audio. Every write
  to a leg (pump, `PlayTo`, MOH) goes through one `write` that protects when
  the leg is encrypted and drops, never sends in clear, if it cannot.
- **Offer shape follows the policy.** `optional` offers `RTP/AVP` with the
  crypto lines (a phone without SRTP ignores them and answers plain, which is
  what Grandstream "enabled but not forced" does); `required` offers `RTP/SAVP`.
  An answer mirrors the offer's profile, and later SDPs for a leg keep the
  profile it negotiated (a transport profile must not change inside a dialog).
  The suites offered are `AES_CM_128_HMAC_SHA1_80` then `_32`; `AES_256_CM_..._80`
  is accepted when asked for, never offered (the spec calls it optional; no
  phone in scope needs it). Lines with an MKI or unsupported session
  parameters are skipped. A lifetime is accepted and ignored.
- **TLS only.** A leg is eligible when its policy is not `off` and its signalling
  is TLS: the caller's `clientTransport`, a callee binding's `bindingOnTLS`
  (the decision "SIP over TLS terminates at the edge"). Trunk legs and external
  URIs are UDP, so they are never eligible: a trunk's `optional` behaves as
  `off`, and `required` refuses calls over it (the trunk policy is stored so it
  takes effect once trunk TLS exists). It does not depend on SIP TCP (#258).
- **Policy storage.** Migration `00052_srtp_policy.sql`: `devices.srtp`
  (default `optional`), `trunks.srtp` (default `off`), `cdrs.srtp` (`none`,
  `caller`, `callee`, `both`). The device default is optional because the spec
  provisions WP phones optional once TLS is deployed, and nothing is offered
  off TLS anyway.
- **Refusals (488).** The caller gate runs before routing: a `required` caller
  not on TLS or without usable crypto, or an `RTP/SAVP`-only offer on a leg
  Hello cannot encrypt (UDP, or policy off). A `required` callee is not rung
  when it is not on TLS, and a `required` callee whose answer lacks crypto is
  BYEd and the caller gets 488. `optional` falls back to plain RTP per leg and
  counts `hello_srtp_negotiations_total{result="plain"}`. The previous
  behaviour for `RTP/SAVP` offers (not parsed, so the call went direct with the
  crypto passed through) is kept only where a call is direct, which is only the
  anchor-failure fallback now that every call anchors.
- **No downgrade after a re-INVITE or a takeover.** Hold, resume and refresh
  answers and offers repeat the leg's key (`keepSecurity`). Replicated dialog
  state carries the leg's mode (`DialogLeg.SRTP`: `savp` or `avp`) and the
  far end's SDP with its `a=crypto` lines removed, so no key is stored in
  Valkey; a taker offers fresh keys in the same profile, and a leg that does not
  answer with crypto fails (one-sided close) instead of continuing in clear.
- **Handed-over legs restart plain.** A transfer target, a retrieved parked
  call, a pickup and the conference loopback take over a relay leg from
  another endpoint, so the leg's keys are reset (`ResetCrypto`) and that party
  is offered plain RTP. Negotiating crypto with those parties is not built.
  Voicemail, paging and the conference mixer (their own media objects, not
  relay legs) answer plain RTP too. A phone forced to SRTP cannot use them;
  recorded here as a known gap, not a hidden one.
- **Provisioning.** Only Grandstream is written: `P183` for a phone on TLS
  (`0` off, `1` optional, `2` required), plus `P60078 = 0` (SDES) where the
  template has it. Checked against Grandstream's own configuration templates
  (config-template-10212026.zip, 2026-11-09 review): `P183` "SRTP Mode" with
  `0` disabled, `1` enabled but not forced and `2` enabled and forced is in
  `wp8x6_config_1.0.3.39`, `wp810_822_825_config_1.0.11.83`,
  `wp820_config_1.0.7.90` and `wp856_config_1.0.3.16` (confirmed; also
  `ht813_config_1.0.19.6`). The WP8x6 and WP810/822/825 add `3` Optional and
  `4` Follow SIP Transport; the WP820 has `3` Follow SIP transport and no
  Optional; the WP856 stops at `2`. Hello writes only `0`, `1` and `2`, so the
  mapping holds on every WP. Which of `1` ("enabled but not forced") and `3`
  ("Optional") is the mode that offers `a=crypto` on an RTP/AVP line is not
  stated by a template, so the choice of `1` stays a hardware check. Newly
  found: `P60078` "SRTP Key Exchange" (`0` SDES, `1` DTLS, default 0) in the
  WP8x6 and WP856 templates, written `0` next to `P183`; the WP810/822/825 and
  WP820 templates have no such key. `P2383` SRTP Key Length (default `0`, both
  AES-128 and AES-256) and `P2363` Crypto Life Time stay at their defaults:
  Hello offers AES_CM_128, accepts AES-256 and ignores a lifetime. Account 2 is
  `P443` and account 3 `P543` (WP856: `P643`, `P1743`, `P1843`), written with
  the lines of item 21. No vendor documentation file for Yealink, Poly, Fanvil
  and Snom is in the repository, so they write nothing until their key is
  confirmed (the Snom TLS precedent).
- **Metrics and CDR.** `hello_srtp_legs` (gauge of encrypted legs, moved with
  the relay across a transfer), `hello_srtp_negotiations_total{result}`
  (`srtp`, `plain`, `refused`); the CDR `srtp` column, the CallDetail card and
  the CSV export. A packet that fails authentication is counted per direction
  in the relay stats (`AuthFailed`) and never latches the leg.
- **Keys are never logged.** `media.Crypto` redacts itself under every fmt verb
  and in slog; traces name the leg and suite only (`TestSRTPKeysNeverFormatted`,
  and the call test checks the CDR trace).

## SRTP SDES: the known gaps of #266 closed (2026-11-09)

Follow-up to "SRTP SDES on the anchor". Choices:

- **One SRTP state type for every media object.** `media.Secure` (SDES
  negotiation plus the SRTP contexts, with `Protect`/`Unprotect`) is what each
  relay leg already was; the relay leg now embeds it and a voicemail session
  (`Anchor.AnswerSecure`), a conference participant (`NewPartySecure`) and a
  page (`Fanout.CallerSecure`, one `Secure` per member via `AddSecureTarget`)
  carry their own. Same keys-per-leg rule, same redaction, same drop of anything
  that fails authentication, no second implementation to keep in step.
- **Voicemail, conference and paging use the caller leg's policy**
  (`call.soloPolicy`: allowed = policy not off and the leg is TLS, required =
  policy required). The answer mirrors the offer's profile. A caller that
  requires SRTP and cannot get it is refused with 488, never answered in clear.
  A conference participant has its own SRTP session, so the mixer encrypts the
  mix per participant. A page encrypts per member: each member's contact is
  offered its own keys under its device's policy and transport (RTP/SAVP when it
  requires them), a required member that is not on TLS is skipped
  (`srtp_required`), one that answers without crypto is hung up. A page's
  multicast copy stays plain RTP: one stream to a group address cannot carry
  per-listener keys; multicast paging is a deliberate plain-RTP feature.
  The test page (`pagetest.go`) follows the same member rule.
- **A parked call sent to voicemail keeps its encryption**: the voicemail
  session is offered keys in the profile the party's relay leg had, and the
  call ends if the party does not answer with crypto.
- **HA for solo calls.** The replicated mode (`avp`/`savp`) now covers
  voicemail and conference calls (`call.soloSec`); the taker offers fresh keys
  and a caller that does not answer with crypto ends the call (zombie count)
  instead of continuing in clear. Hello's own key (the `SDP` of the leg, which
  now carries one) is stripped before replication, as the far end's was.
- **Hand-over legs negotiate again** instead of restarting plain. A transfer
  target (blind and attended), a retrieved park, a pickup and the parker's
  device on a park return are new endpoints on a relay leg: the previous
  endpoint's keys are dropped (`ResetCrypto`) and the new party is offered, or
  answered with, keys under its own policy and transport (`cryptoOfferOn`,
  `cryptoAnswerOn`, `learnCryptoOn`). A party whose policy is `required` and
  that cannot be encrypted is refused (488, or the call ends); an `optional`
  party that does not answer with crypto is plain RTP, counted as `plain` and
  traced. A pickup now also answers the ringing caller's SDES offer (the caller
  had only been given a key when the leg was already negotiated).
- **Roaming is not a hand-over.** `relearnMedia` (the same phone at a new media
  address after a re-INVITE) called the hand-over helper and so dropped the
  leg's keys, silently continuing in clear. It now uses `moveLeg`, which
  re-aims the leg and keeps the SRTP state.
- **Across nodes, retrieve and pickup are plain or refused.** The retriever's
  offer travels to the holding node through the shared store, and a key must
  not be written there (the replication rule), so its `a=crypto` lines are
  dropped: an `optional` phone is plain RTP (counted, traced), a phone that
  requires SRTP or offers `RTP/SAVP` only is refused with 488 before it takes
  the slot. Same-node retrieve and pickup negotiate fully.
- **A trunk cannot be set to `required`** (gap 3). The trunks-routing spec lists
  SIP over TLS for trunks as a later phase, so there is no trunk TLS transport
  to require. Rather than build one inside an SRTP change, the state is made
  impossible: `POST`/`PATCH /api/v1/trunks` answer 422 on `srtp` when it is set
  to `required` (only the value being set is checked, so a trunk stored as
  `required` earlier can still be edited and fixed), the Trunks page offers
  `off` and `optional` only and says why, a stored `required` is flagged on its
  card and refuses to save as it is, and the OpenAPI text and the skills say
  so. No migration: existing rows are not rewritten, since turning `required`
  into `off` behind the operator's back would be a silent downgrade. Calls over
  such a trunk keep failing with 488, and the CDR trace says why. When trunk TLS
  exists this check becomes "required needs a TLS trunk".
- **Not changed.** An in-dialog re-INVITE that rekeys a voicemail or conference
  caller is answered with the call's existing key (a phone that rekeys on its
  own re-INVITE is followed on relay legs only). AES-256 is still not offered.

## DTLS-SRTP and ICE-lite on the anchor (phone-feature-parity item 25, S-16) (2026-11-09)

Spec: `phone-feature-parity.md` S-16, with `clients-webrtc-mobile-video.md` S-4
(same code, delivered once). Stacked on the SRTP gaps PR (#274). Choices:

- **Modules.** `pion/dtls/v3` v3.1.5 (the handshake, over a packet conn fed from
  the leg's receive loop) and `pion/stun/v3` v3.1.6 (STUN parsing, integrity,
  fingerprint) join `pion/srtp/v3`; `pion/transport/v4` moves to v4.0.2 as they
  require. The spec names `pion/ice` too. It is not used here: ICE-lite is a
  fixed answer (host candidate out, authenticated binding requests answered),
  and `pion/ice`'s agent gathers, runs its own sockets and wants to own the
  UDP port, which fights the relay's per-leg sockets. The WebRTC phase decides
  about `pion/ice` and its UDP mux when it needs one shared port; nothing here
  blocks it.
- **One per-leg crypto layer.** The DTLS handshake fills the same `legKeys` SDES
  fills (`Secure`), so the relay, recording, announcements, voicemail, the
  conference mixer and the page caller keep one `Protect`/`Unprotect`. A
  leg's socket now carries STUN, DTLS and RTP, told apart by the first byte
  (RFC 7983); `Secure.UnprotectFrom` is the one place that does it. A DTLS leg
  with no keys yet carries nothing in either direction: it can never fall back
  to plain RTP.
- **What to offer and accept when.**
  - Answering mirrors the offer: `UDP/TLS/RTP/SAVP(F)` with a fingerprint gets
    DTLS-SRTP, `RTP/AVP` or `RTP/SAVP` with `a=crypto` gets SDES (S-15). A
    fingerprint on an `RTP/AVP` line is ignored, not read as an offer.
  - Offering follows the device: new `devices.srtp_keyex` (`sdes` default,
    `dtls`). `off` offers nothing; `optional` and `required` offer the chosen
    exchange (`required` = SAVP-only semantics as before). A DTLS transport
    cannot carry a plain fallback inside the same offer, so a device set to
    `dtls` is for phones that do DTLS-SRTP; there is no dual SDES+DTLS offer
    (no vendor documentation says phones accept one).
  - **TLS only, as SDES.** DTLS carries no key in the SDP, but the fingerprint
    is the only thing that authenticates the peer certificate, so it is trusted
    only when it arrived over protected signalling (the decision recorded under
    S-14: SRTP and DTLS-SRTP are offered only where `bindingOnTLS` or the
    caller's `clientTransport` is TLS). The same `off`/`optional`/`required`
    policy gates it. A DTLS-only offer on a UDP leg, with policy `off`, without a
    usable fingerprint, or on a node with `HELLO_MEDIA_DTLS=false` is refused
    with 488.
  - A required callee that answers without DTLS ends the call (488), as for
    SDES. A trunk never carries DTLS (trunk legs are UDP).
- **Roles.** Hello answers `active` (client) to `actpass` and `passive`, and
  `passive` to `active`, as RFC 5763 section 5 says (SHOULD be active); when it
  offers it sends `actpass` and takes the role the answer leaves. A client waits
  until the peer address is known and, with ICE, authenticated.
- **Fingerprints.** `sha-1`, `sha-256`, `sha-384`, `sha-512` accepted for the
  peer (matching any listed one; sha-1 is only compared against a certificate
  the peer must already hold, a second-preimage problem, and some phone stacks
  only send it); md5/md2 refused. Hello's own is sha-256. The extended master
  secret is requested, not required (phones on an old TLS stack lack it, and the
  certificate is pinned by fingerprint either way); the WebRTC leg should
  require it. SRTP profiles offered: AEAD_AES_128_GCM, AES128_CM_HMAC_SHA1_80,
  AES128_CM_HMAC_SHA1_32. GCM is a DTLS-only suite: SDES still offers the two
  CM suites and refuses a GCM `a=crypto` line.
- **Certificate.** One self-signed ECDSA P-256 certificate per hello-sip process,
  made on first use and replaced for new sessions after 30 days (valid 90),
  never stored or shared (a restart or takeover gives a new fingerprint; the
  taker's re-INVITE carries it). A session keeps the certificate it started
  with. The peer sees only the fingerprint.
- **Failure.** A handshake that times out (8 s), presents a certificate that
  does not match, or fails is counted, traced and ends the call (BYE to both
  legs, or an abort during setup). There is nothing to fall back to: the offer
  or answer was DTLS only. (The spec's "otherwise falls back to RTP" is read as
  the negotiation: an answer without DTLS from an `optional` device is plain
  RTP, as with SDES.) Voicemail greetings and announcements wait for the
  handshake instead of losing their first frames.
- **ICE-lite.** Answers an offer's ICE credentials with `a=ice-lite`, its own
  ufrag and pwd per leg, one host candidate (the relay address, component 1) and
  `a=rtcp-mux`; always controlled (487 to a peer that is also controlled);
  binding requests are answered only with the right USERNAME and
  MESSAGE-INTEGRITY, everything else is dropped silently (no oracle). A valid
  check vouches for its source address (the leg then accepts media from it) and
  the first or nominated pair becomes where media goes. Remote `a=candidate`
  lines are parsed and kept, never checked. ICE does not need TLS (it carries
  no secret) and does not encrypt. Answering ICE has no per-device switch: a
  phone that sends ICE credentials expects an answer, and `HELLO_MEDIA_ICE=false`
  turns it off node-wide. The device's `media_ice` only decides whether Hello
  offers ICE-lite and what provisioning writes. The far end's ICE password is not
  replicated for HA.
- **rtcp-mux** is answered on DTLS and ICE legs only (RTCP on the media port is
  consumed). Plain legs are unchanged: no mux claimed, nothing to drop.
- **Hosts.** The relay legs, the voicemail/IVR session, the conference
  participant and the page caller leg all bind their socket to their `Secure`.
  Page members are offered SDES, never DTLS (a member socket only sends). A
  retrieve or pickup that crosses nodes with a DTLS leg is refused with 488 (the
  shared-store command carries neither the retriever's policy nor its
  transport), like a required SDES one. The replicated leg mode gains `dtls`.
- **Provisioning (vendor docs, Grandstream WP8x6 config templates).** `P60078`
  SRTP Key Exchange of account 1 (`0` SDES, `1` DTLS; WP826, WP836, WP856),
  written with `P183` for a TLS phone; `P95002` Media NAT Traversal of account 1
  (`1` Auto, the documented default, `4` ICE; WP826 and WP836, the WP856
  template does not list it), written for a phone of those models whatever its
  transport. No other vendor's key is confirmed, so none is written.
- **Migration 00079** (highest on #266 is 00078): `devices.srtp_keyex`,
  `devices.media_ice`, `cdrs.srtp_keyex` (`none`, `sdes`, `dtls`, `mixed`),
  `cdrs.ice`.
- **Metrics.** `hello_dtls_handshakes_total{result}`,
  `hello_dtls_handshake_seconds`, `hello_ice_checks_total{result}`;
  `hello_srtp_negotiations_total` gains `dtls` and `dtls_failed`.
- **What the WebRTC phase gets.** The ICE-lite agent, the DTLS handshake over a
  demultiplexed socket, the node certificate and fingerprint, the GCM profile,
  `a=rtcp-mux` and the SDP parsing of `fingerprint`, `setup`, `ice-*`,
  `candidate` and SAVPF. Still to build there: the multi-section SDP model,
  BUNDLE, `a=mid`, the shared WebRTC UDP port with a ufrag demultiplexer, the
  public candidate address, trickle/`end-of-candidates` and the Kamailio
  WebSocket and TURN pieces. No port or chart change is needed for phones.
- **Needs hardware (lab item 10, WP822/WP836).** That `P60078 = 1` makes the
  phone offer `UDP/TLS/RTP/SAVP` with a fingerprint and `a=setup:actpass`, which
  fingerprint hash and SRTP profile it uses, that it handshakes as client against
  Hello's `active` answer and as server against Hello's offer, what its answer
  to Hello's offer says in `a=setup`, and what "enabled but not forced"
  (`P183 = 1`) does together with DTLS; that `P95002 = 4` makes it send ICE
  credentials and candidates, accept the lite answer and send its checks to the
  relay address (Trickle ICE `P95003` on and off); that hold, resume and a
  re-INVITE keep the session; and that a WP822 ignores the keys it does not have.

## Locations, HELD, Geolocation, callback tag and alarm `*55`, phone-feature-parity S-28 and S-29 (2026-11-09)

- **Already built, left as it was.** The emergency flag on outbound routes and
  its bypass of the trunk call limit and a down trunk (`acquireSlot`), P8385 and
  the ring tag constants (`alarm`, `emergency-callback`). The caller's DND and
  forwarding never act on a call out to a trunk, so (a) needs nothing there. The
  door extension's target list (S-45) is not on main; the bypass is documented
  for it to honour when it lands.
- **Locations** (migration 00052): one table, kind site, floor or room, parent
  RESTRICT. The civic address is on the site only; a floor's name is `FLR`, a
  room's `ROOM`; the most specific geo point on the path wins. The user asked for
  locations assignable to devices and extensions, the spec for a group with an
  extension override; both are built, plus the phone itself, and the most
  specific wins: phone, then extension, then phone group. Assignments are one
  `location_id` column on `phone_groups`, `extensions` and `phones` (RESTRICT),
  written through `PUT /location-assignments/{kind}/{id}`; a delete names what
  holds the location. Resolution is the pure `internal/location` package, used by
  the store, the snapshot and the HELD server.
- **HELD**: `POST /p/<token>/held` on the provisioning listener, through the same
  `authorize` as a config fetch (token, HTTPS, allowlisted phone; every denial an
  empty 404). The body is parsed (root `locationRequest`, no DOCTYPE, 8 KiB) but
  never picks the location. Answers are `application/held+xml` with status 200
  also for HELD errors (`locationUnavailable`, `requestError`), the usual client
  behaviour; a Store without `LocationSource` answers 404, so the fake stores of
  other tests need no change. No audit kind was added: a HELD request is audited
  as kind `other`, result `served` (the fetch table's CHECK lists would need a
  migration for nothing).
- **Vendor keys, from Grandstream's own documents only.** Fetched
  `config-template-10212026.zip` (`wp8x6_config_1.0.3.39.txt`) and the WP8x6
  Administration Guide (documentation.grandstream.com). Written on a WP8x6 with
  E911: P8565 = 1, P8566 = 1 (HTTPS), P8567 = 60, P8568 = location server,
  P8597 = emergency numbers separated by ";" (the guide's delimiter), P8725 = 1
  (RFC 6442 header format), P26059 = 1. With an alarm destination: P22269 = 1,
  P22270 = `*55`, P22601 = 1, P22602 = 1. Not written, because neither document
  fixes the value: P8574 (location types; the template spells it
  `Geodetic,civic,locationURI`, the guide differently), P8576, P8577 to P8596,
  P8598/P8599. Unconfirmed: the form of P8568 ("address"): written as
  `host/p/<token>/held` without scheme, as P331 takes its path; a phone check
  settles it. No other vendor was changed: the spec names none and no vendor
  document in hand has an E911 key set.
- **Geolocation on trunks** (migration 00052 `trunks.geolocation`, off). Only an
  emergency call, only a flagged trunk. The phone's own PIDF-LO (multipart
  part, well-formed XML with a `presence` root and no DOCTYPE, 16 KiB) is
  forwarded in place of the stored one; a location by reference in the phone's
  header is passed on; `Geolocation-Routing` and `P-Emergency-Info` are passed
  on, never generated (their value is the phone's). The caller's INVITE is
  reduced to its SDP part at the start of every call, so the media code never
  sees a multipart body. **UDP size**: sipgo refuses datagrams over 1300 bytes
  and an INVITE with a PIDF-LO does not fit; the package-level limit is raised to
  4000 in an `init`, before any server exists (no race), so the request leaves
  and fragments. The PIDF-LO is as small as RFC 4119 allows (only the namespaces
  used, no timestamp). The risk is a carrier that drops fragments; that is why
  the flag is per trunk and off. The alternative, leaving the location out,
  would make the flag useless on UDP.
- **Callback tag** (f): `hello:emergency:<ext>` in Valkey (30 min) plus a
  node-local map, written when a call over an emergency route is routed. A call
  that entered on a trunk and rings that extension gets ringKind
  `emergency-callback` and ignores DND; the CDR kind stays `call` (its CHECK
  has no such value, and the callback is a plain call). Only a trunk call counts:
  a colleague's call is not the callback.
- **Notify group** (g): `emergency_settings.notify_group` (one row), a ring group.
  Its phones are paged like the paging-group test (auto-answer headers, sendonly
  offer) and hear "Emergency call from <name>, extension <digits>" in the
  deployment's prompt language, a tone when speech is off. DND is not honoured
  (a notice, not a call); the calling extension is skipped.
- **Alarm `*55`**: handled in `handleInvite` before the paging, conference and
  feature-code lookups, so a feature code or paging code on `*55` can never shadow
  it; the dial-space guard counts `*55` as a built-in code, so a new overlap is
  refused (an existing one does not block unrelated changes). Kind `alarm` (CDR
  CHECK widened), ring tag `alarm`, `ignoreDND` on the call (extension DND and
  the DND of ring group members), "Panic alarm from <name>" played to the callee
  leg only after the answer, 404 without a destination or with a deleted one.
  Forwarding of the destination still applies.
- **Collision check against origin/main and the open PRs** (2026-11-09): codes
  taken are `*28 *40 *41 *42 *45 *46 *47 *48 *70 *71 *74 *75 *80` (main), `*76`
  `*77` (#272), `*64` (#236), `*63` (#220), `*85` as a paging code (#225 test);
  nobody takes `*55`. Migration 00052 is also taken by #272, #270, #266 and #238:
  whoever merges second renumbers (and the three migrate counts move by one).
- **API roles**: locations and assignments write/operator like the ring map and
  tags; a phone group's E911 switch and alarm destination, and the emergency
  settings, admin/admin like the phone group changes. They bump the
  configuration revision (hello-sip reads them).
- **Not done**: a HELD location URI, LLDP or NAI based lookup, E911 keys for other
  vendors, the door target list bypass (waits for S-45), an UI picker on the
  Phones page (the Assignments tab does it).

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

## Flow canvas P6: AI on the canvas (2026-11-09)

Built per `.procoder/specs/flow-canvas.md` (P6, S-17, S-19, S-20). Choices the
spec left open, and one deviation:

- **The assistant may apply its own batch (decision 2, user decision of
  2026-11-10 below).** It has one write tool, `applyConfigBatch`, offered only
  for a question asked with the canvas switch "Let AI apply changes" on;
  otherwise a change is a proposal whose batch the operator previews on the
  canvas and applies or discards. Write-scoped MCP clients keep
  `applyConfigBatch` as before.
- **A batch proposal is one action.** `applyConfigBatch` joins
  `proposal.Allowlist`; a proposal that carries it has no other action, so the
  whole change is one transaction. The inner allowlist is the batch's own
  table (`configbatch.Ops`, every object type the canvas draws); `createDevice`,
  `createPhone`, credential, multipart and external-effect operations stay
  out. At most 50 inner operations.
- **Credentials stay out of proposals.** Decision 2 allows the AI to set
  secrets with redacted reads; a proposal's body is stored and shown to
  people and to the model, so an inner operation that sets a password, PIN or
  secret is refused, as for every other proposal (S-12).
- **The dry run runs as the user, rolled back.** The validator dry-runs the
  batch through the API as the user's agent identity with a new `Preview`
  flag (`auth.Agent.Preview`): it then holds write scopes with the user's own
  role, but the API admits its writes only on `POST /api/v1/config/batch`
  with `dryRun` and inside a dry-run store batch (`agentGuard`,
  `Batch.DryRun`). A viewer's or an operator's admin-only batch is refused
  there and the model is told the user cannot make the change.
- **Hello fills the staleness tokens.** The model does not send
  `baseRevision` or `expect`: the validator reads the graph (devices
  included) and sets `baseRevision` to its revision and each update or
  delete's `expect` to the node's version. A configuration that moves under
  the proposal makes the batch's own check answer 409, which makes the
  proposal `stale`; changes to other objects do not.
- **The preview is stored.** The dry run's results (redacted), warnings and a
  new `flowDiff` (added, changed, removed nodes, edges added and removed,
  `flow.Diff`) are kept on the action as `preview`, so the overlay and the
  proposal page need no write scope and viewers see them. Every dry-run batch
  answer now carries `flowDiff` (documented in `openapi.json` and
  `docs/config-batch.md`).
- **Overlay placement.** Created nodes have dry-run ids (sequences do not roll
  back), so the canvas draws them as ghosts placed next to a connected node
  (not draggable) and, after apply, places the real nodes at the ghost
  positions by matching kind and label (`placementsAfterApply`).
- **Canvas context.** A question carries a `context` with `canvasId`,
  `nodes` and `trace`, stored in `ai_messages.context` (migration `00057`) so the task and
  the history see it, and given to the model as data (`canvas`), never as
  instructions. The panel sends the canvas, the selected node and the traced
  call.
- **`describeOperation`.** One assistant tool that is not an API read: it
  answers the request schema of an operation a proposal may carry, from the
  embedded document, so the model writes valid batch bodies. It is not an MCP
  tool and reads no PBX data.
- **`getFlowGraph` `fields=summary`** (S-17) empties node summaries and drops
  edge `data`, keeping ids, labels, versions, ports and owners.
- **Roles.** Asking needs operator (the chat already did); viewers see the
  panel's proposal list and the overlay, without Apply. Discard is
  `dismissAIProposal` with reason `not_needed`. With the agent off the panel
  shows the same reason as every AI page.
- **Import cycle.** `configbatch` used `proposal.Change`; the JSON diff moved
  to `internal/jsondiff` (proposal keeps aliases) so `proposal` can use
  `configbatch`.

## Integrations P4: recording links and lookup (2026-11-09)

- Links are Hello-signed proxy URLs (`/r/<token>.wav`), not MinIO presigned
  URLs: the object store endpoint is cluster-internal. The token is 256
  random bits, base64url (43 characters); only its SHA-256 is stored.
- All refusals of `/r/` (unknown, expired, revoked, used up, malformed) are
  the same 404, so a token's history is not revealed; the reason is counted
  in `hello_recording_link_hits_total{result}`. Rate limit is 30 requests per
  minute per token and address, plus 300 per address, in memory per replica.
- `maxUses` counts every request, including a player's Range requests, so a
  forged `Range` cannot get past the limit. Default is 0 (unlimited).
- Who may mint: a user or any credential with an `extensionScope` only for
  calls involving its extensions; a viewer likewise (their linked
  extensions), since the spec says operator/admin for any call. A recording
  whose CDR is not written yet can only be linked by an unrestricted
  credential. Revoking needs the same, or being the creator.
- Links die with their recording through `ON DELETE CASCADE` (migration 00052)
  rather than a trigger. `connector_id` is a plain column until the CRM tables
  exist (P6).
- The lookup API is two operations, `GET /lookup/calls` (scope `calls`) and
  `GET /lookup/recordings` (scope `recordings`), both with the `user` role and
  the extension restriction in SQL. The spec's contact lookup
  `GET /lookup/number` (I-43) is a separate feature (phonebook, not calls) and
  is not part of this PR. Number matching is digits-only with a last-eight-digit
  suffix match from eight digits (not `phonebook.Dialing.Key`, which needs the
  dial context and would hide calls recorded in another format; eight, not the
  phonebook's nine, because the national form's trunk prefix 0 is dropped in
  the international one, so a 9-digit national number shares only 8 digits).
- Not built because they cannot work as written: `GET /cdrs/{id}/recording-link`
  (a token cannot be shown again, only its hash is stored; the lookup returns
  `recordingId` and `POST /recordings/{id}/links` mints) and the CDR fields
  `origin` and `origin_client` (they belong to P3, click-to-call, which
  is unmerged). `recordingId` is on the lookup results and on
  `GET /cdrs/{id}` (not on the list).
- Minting and revoking are excluded from MCP (they hand out a public URL to
  call audio); the lookups are MCP tools, taught in the hello-troubleshoot
  skill.

## SCEP and 802.1X, item 37 (S-25) (2026-11-09)

Builder's design choices, within the spec; nothing here was asked of the user.

- **Hello is the SCEP server** (RFC 8894), as S-25 says, not a SCEP client
  pointed at an outside CA. An enterprise PKI is served by importing one of
  its intermediate CAs (certificate and RSA key) as the device CA, so RADIUS
  already trusts what Hello issues.
- **Library:** `github.com/smallstep/scep` (the SCEP code step-ca uses,
  maintained, last change 2026-10-14) with its `github.com/smallstep/pkcs7`;
  no hand-rolled PKCS#7. This amends the spec's "one new module" rule for
  this item, on the brief's instruction to prefer a maintained SCEP library.
- **Device CA, separate from the provisioning CA.** The provisioning CA
  (HELLO_PROV_CA_CERT) is cert-manager's `cluster-ca` on kw and Hello holds
  only its certificate (the SIP TLS certificate of #202 is issued by
  cert-manager too), so it cannot sign. Hello keeps its own device CA in
  `scep_ca`: generated (RSA 3072, self-signed, 10 years) or imported; RSA
  because SCEP clients encrypt to the CA certificate. The key is sealed under
  HELLO_SECRET_KEY (`scep-ca:<id>`), one CA active, a replaced one kept for
  its certificates' rows.
- **Switches:** SCEP is off per phone group by default (S-25);
  `HELLO_SCEP` defaults to `true` and is a deployment-wide kill switch, so the
  feature needs no deploy change on kw (the `/p/` path is already routed).
- **Endpoint:** `/p/<token>/scep` (also `/scep/pkiclient.exe` and
  `/scep/cgi-bin/pkiclient.exe`), GET and POST, HTTPS and an allowlisted phone
  like a config fetch; a group with SCEP off answers the empty 404. The CA and
  CRL are public at `/p/scep-ca.crt` and `/p/scep-ca.crl` (plain HTTP too,
  for RADIUS).
- **Challenge:** one per phone, sealed (`scep-challenge:<phone id>`), made at
  the first render after SCEP is on, consumed by the enrolment it authorises
  (row-locked, so two racing enrolments get one certificate), and replaced at
  the next render. A retried transaction (same phone, CA and key) gets its
  certificate again without a second issue.
- **Renewal:** RenewalReq/UpdateReq signed by a live certificate Hello issued
  to the same phone from the active CA; no challenge.
- **Issued certificate:** CN = the phone's MAC, nothing taken from the
  request's subject or SANs; client auth; CRL distribution point; the group's
  lifetime (default 365 days, 30 to 1825). Weak keys refused (RSA < 2048, EC
  other than P-256/P-384). The CertRep is encrypted with AES-128-CBC (set on
  the pkcs7 package variable; GetCACaps advertises AES, SHA-256, POST,
  Renewal, SCEPStandard).
- **Revocation:** admin revokes from the API/UI; removing a phone revokes its
  live certificates (trigger). The CRL is signed per request, valid 24 h, its
  number the Unix time.
- **Vendor keys, Grandstream templates only** (config-template-10212026.zip):
  SCEP on the WP8x6 (wp8x6_config_1.0.3.39) and GRP2601-2604
  (grp260x_config_1.0.7.74), P8647/P8648/P8653/P8654/P8655, P8647 = 0 when
  off; P8652 (fingerprint, format undocumented), P8662 and P8663 not written.
  Wired 802.1X on the GRP26xx (grp26xx 1.0.15.23, grp261x 1.0.13.127,
  grp260x 1.0.7.74): P7901/P7902/P7903/P8439, written only while the group
  names a profile (never switches 802.1X off a port), EAP-TLS only where SCEP
  is on, P8440 not written. WP856/WP8xx have no SCEP keys; Yealink, Poly,
  Snom and Fanvil get nothing (no confirmed vendor keys at hand).
- **802.1X model:** `dot1x_profiles` (name, method eap-tls/peap/eap-md5,
  identity with {mac}/{MAC}, anonymous identity, sealed write-only password
  `dot1x:<id>`, RADIUS CA PEM, server name). A phone group names its wired
  profile; Wi-Fi profiles (item 31) reference a profile by id with
  `ON DELETE RESTRICT` and read the resolved profile from
  `RenderData.Dot1X.Profiles` (WP8x6 P83004/P83016 values precomputed).
- **API:** `GET /scep`, `PUT /scep/ca`, `GET /scep/certificates`,
  `POST /scep/certificates/{id}/revoke`, `POST /phones/{id}/scep-challenge`,
  `/dot1x-profiles` CRUD, `GET|PUT /phone-groups/{id}/security`; viewers read,
  admins change (S-25 RBAC). UI: Phones > Certificates, and "New SCEP
  challenge" on the phone page.

## Clients 3 and 4: web devices, coturn and ICE servers (2026-11-09)

Spec: `.procoder/specs/clients-webrtc-mobile-video.md`, phases 3 and 4 (S-13 to S-17, S-21 to S-24). One PR: the two share the configuration, the credentials Secret and the chart wiring, and neither depends on the media foundation (#276) or the WSS edge.

- **Derived credentials, stored under the base name.** A web device row stores `sip_username = w<id>` and no HA1 (`auth_mode = derived`, HA1 columns nullable); the client presents `w<id>.<expiryUnix>`. `Snapshot.DeviceByUsername` resolves the expiring name to the base device (only when that device is derived, so a desk device named like `w99` is never reached through the derived path), and bindings are keyed by the base AOR, so a refreshed credential replaces the old binding. Revocation is `enabled = false`; the snapshot then drops the device and `SnapshotChanged` removes its bindings.
- **Browser session only.** The five new routes use `ScopeSession` (a bearer token, an OAuth grant or a service account is refused) because the SIP password is minted there. The spec's `softphone` scope is for the mobile pairing flow (S-27, not now). They are excluded from the MCP tools (category `credentials`), so no new tool and no skill teaching is needed; the skills mention the `kind` filter of `listDevices` and the 409 on web devices.
- **Only `web` is enrolable.** `devices.kind` allows `mobile` so the schema does not change when the apps come, but the API refuses `kind: mobile` (decision of 2026-10-25: apps not now).
- **Revoke disables, the list shows active only.** Following S-14. The cap counts active client devices, under a lock on the user row (a concurrent test proves it).
- **Account changes revoke devices.** Disabling or deleting a user, or unlinking an extension from a user, disables the user's client devices on it in the same transaction and moves the revision, otherwise a removed person would keep a working softphone until its credential expired.
- **Ring preferences.** `users.ring_web` and `ring_mobile` (default on) reach hello-sip as `Device.RingOff` in the snapshot; `bindingsFor` skips such a device before forking. Every fork that is cancelled because another answered now carries `Reason: SIP;cause=200;text="Call completed elsewhere"` (it was pickup only).
- **`last_seen_at` is the last enrol or refresh**, not the last REGISTER: hello-sip does not write to PostgreSQL on a registration, and "registered now" is read from the live bindings.
- **Not in this PR, with the reason.** `cdrs.video` and the ICE candidate type in the CDR (there is no video or WebRTC leg yet; the trace names the web device that answered); the Diagnostics "Clients" tab and `GET /api/v1/webrtc/status` (S-52, nothing to count before the WSS edge and the softphone; REGISTER attempts of web devices already show in the existing per-device diagnostics); the pairing code and `softphone` scope (mobile). These belong to items 2, 5 and 6.
- **TURN secret and key are environment values from the credentials Secret** (`turnSecret`, `clientAuthKey`, `clientAuthKeyPrevious`), referenced by the chart and never rendered; "sealed" is the sops Secret on kw, as for `secretKey`. They are never logged or put in an audit row (tests check it). Rotating the TURN secret needs a rolling restart of control and coturn together (coturn has no multiple concurrent secrets that were verified).
- **coturn.** One Deployment per address, one replica, `Recreate`, `hostNetwork` by default (the relay leaves from the node address that is `externalIP`); without hostNetwork each server is a LoadBalancer Service listing at most 200 relay ports. `allowedPeers` is required, so the chart cannot render an open relay. The Prometheus exporter is off by default because the Debian build was not checked. `ct-<tree hash of docker/coturn>` is the content tag, like `kc-` for Kamailio.
- **ICE servers.** `GET /api/v1/me/ice-servers[?deviceId=]`, 60 requests a minute per user (in-process window, per node), counted by `hello_ice_credentials_issued_total` and not audited per credential. STUN defaults to the `turn:` hosts.
- **Migration 00075** (web devices; the highest on main is now 00074). `test/integration/migrate_{prov,ai,ai_agent}_test.go` counts bumped by one.

## Wi-Fi profiles and roaming (item 31, S-23, S-24)

Keys come from Grandstream's official templates only (config-template-10212026.zip):
`wp8x6_config_1.0.3.39`, `wp810_822_825_config_1.0.11.83`, `wp856_config_1.0.3.16`.
Nothing here is confirmed on a handset.

- **Model scope.** WP816/826/836, WP810/822/825 and WP856 get Wi-Fi keys. The
  WP820 has its own template (`wp820_config_1.0.7.90`) and is not written. No
  other vendor is written: no vendor document for their Wi-Fi keys is on hand.
- **Profiles are named records**, assigned to phone groups and to single phones
  (a phone's profile with the same SSID replaces its group's). The spec said
  groups only; phones were asked for. At most 10 per group and per phone; a
  model writes at most its slots (WP8x6 10, WP810/822/825 17, WP856 1), lowest
  priority dropped first.
- **17 networks on the WP810/822/825, not 20.** The template gives networks
  18 to 20 a security key outside the pattern of the other 17 (P84772, P84872,
  P84972 against P83002 + 100 (n - 1)). Unconfirmed keys are not written.
- **No enterprise profile in network 5 of a WP8x6.** The template names its
  EAP method P83304 where the pattern needs P83404; the profile moves to the
  next network.
- **No 802.1X, WPA3 or OWE on models whose template has no key for it.** WP8x6:
  open, WPA2/WPA3 personal, OWE, WPA2/WPA3 enterprise (PEAP, MSCHAPv2).
  WP810/822/825: open, WPA2 and WPA3 personal (no EAP keys in the template).
  WP856: open and WPA2 personal. A profile a model cannot take is skipped for
  that model. EAP-TLS and an uploaded CA wait for S-25 (device certificates).
- **Server certificate check** defaults to system certificates (P83016 = 1);
  trust on first use and skip are offered, uploaded CA is not (S-25).
- **Band is a group radio setting, not a profile field.** The templates have
  one band key per phone (P25438 on the WP8x6, P25420 and P25421 on the
  WP856), none per network. Hidden is written to the WP810/822/825 only
  (P83050), priority and preferred account to the WP8x6 only (P83049, P83085).
- **802.11k is not provisioned.** No key for it in any of the three templates.
  802.11r is P83048 per network (WP810/822/825) and P25315 (WP856); 802.11v is
  P84992 (WP810/822/825) and P82333 (WP856); the WP8x6 template has neither.
  The spec's P22232/P22231 map to all three; P82281 is the VoWLAN target delay
  in the templates (not a roaming threshold), left to S-12 overrides with the
  power-save mode P82269.
- **Safety gate (decision 14) is enforced at render time**: profiles are
  written only when the phone's effective set holds one marked verified. The
  radio settings do not wait for it. The API shows `pushing`. An assigned but
  unverified set is accepted (so the test-phone step can be prepared), and
  writes nothing.
- **Hello owns the slots once a phone has profiles**: slots it does not fill are
  written empty so a removed profile disappears from the handset.
- **P84991 (provisionable OEM override) is never written**, so an OEM Wi-Fi
  setup survives.
- **Sealing**: the PSK and the 802.1X password use the store's secret box with
  contexts `wifi-psk:<id>` and `wifi-password:<id>`; the API shows only
  `pskSet` and `passwordSet`; previews mask them. A change does not bump the
  configuration revision (as the ring map): the automatic resync notices the
  changed render data.
- Migration 00052 (`wifi_profiles`, `wifi_assignments`, `wifi_radio`); the
  migrate test counts follow it.

## Fax (pbx-gaps B5 and B6, S-15 to S-17) (2026-11-09)

Built in one change: the engine, passthrough, T.38, email, API, page and docs.

- **Engine: spandsp through cgo, as D13 decided; there is no pure-Go option.**
  No maintained pure-Go T.30 or T.38 implementation exists (the V.17, V.29 and
  V.27ter modems, T.30 and ECM are the work). Upstream spandsp 0.0.6 is itself
  unmaintained (Homebrew marks it deprecated); Debian and Homebrew still ship
  it and a fork (spandsp3) continues it. The API used is the 0.0.6 one, so the
  engine builds on both. Risk and exit: the C code is confined to
  `cmd/hello-fax/engine_spandsp.go` behind the `spandsp` build tag; moving to
  spandsp3 is a header and a handful of calls.
- **Image: Debian, not Alpine.** The `hello-fax` Dockerfile target builds on
  `golang:1.27-bookworm` (`libspandsp-dev`, `libtiff-dev`) and runs on
  `debian:bookworm-slim` with `libspandsp2`, `libtiff6` and `poppler-utils`.
  Alpine's spandsp3 has a different API and a static musl build buys nothing
  here. The other images are untouched and never link spandsp. CI gains a `fax`
  job (container `golang:1.27-bookworm` plus the packages) that runs the cgo
  tests; publish builds the image after it passes.
- **Pages cross the cgo line as TIFF files.** spandsp's T.30 writes received
  pages to a TIFF and reads pages to send from one; there is no row API on the
  T.30 state. Sending is an uncompressed bilevel TIFF written in Go; receiving
  asks spandsp for T.6 output and decodes it in Go with
  `golang.org/x/image/ccitt` (new module, Go team, BSD). Rows are then encoded
  to PDF with stdlib `compress/zlib`, as D13 says.
- **Control protocol.** hello-sip and hello-control speak HTTP to hello-fax
  (`internal/fax/client.go`): start a session, poll it, fetch the PDF, stop,
  forget. A session opens a UDP port; hello-sip's relay forwards RTP or UDPTL
  there. The response names the pod (`HELLO_FAX_MEDIA_HOST`, the pod IP in the
  chart): media goes to the pod, not through the Service (replies from a pod
  would otherwise fail the relay's source check), and later calls for the
  session go to that pod, so several replicas work. An optional bearer token
  (`HELLO_FAX_TOKEN`) guards the API.
- **hello-sip owns the call and the data.** It answers or places the call,
  runs the relay, writes the `faxes` row, uploads the PDF and records the CDR;
  hello-fax holds no credentials. hello-control owns the API, the email worker
  (through `internal/mailer`, claimed from the table so one node sends each)
  and the retention job (`TryLock`, batches of 500, objects before rows).
- **UDPTL is read and written in Go** (`internal/fax/udptl.go`, redundancy
  error recovery, three secondary packets); spandsp's T.38 terminal sees IFP
  packets only. The relay gained a UDPTL mode (`Relay.SetUDPTL`): datagrams
  cross unmodified, no RTP parse, no sequence rewrite.
- **T.38 is per trunk (`trunks.fax_mode`, default passthrough, D14).** CNG
  (Goertzel, 1100 Hz, three 100 ms windows) on a `t38` trunk re-INVITEs the
  caller to `m=image`; a carrier's own T.38 re-INVITE is accepted the same way.
  Outbound: the engine asks for T.38 from the start of a session on a `t38`
  trunk, and hello-sip re-INVITEs when the carrier answers the call. A 488, a
  port 0 or parameters Hello does not serve (version above 0, local TCF, FEC)
  leave the call on G.711. Calls routed through an IVR key do not watch for CNG
  (the relay is shared with the menu); they still accept a carrier's re-INVITE.
- **Destinations.** A fax destination is a row (`fax_destinations`: name,
  owner extension, optional email) that an inbound route (kind `fax`) or an IVR
  key (kind `fax`) names; admin writes, viewer reads. A destination in use
  cannot be deleted. An IVR key moves the answered call to G.711 with a
  re-INVITE; if the caller refuses, the menu says the choice is invalid.
- **Outbound routing.** A fax is routed as a call from the pseudo source
  `fax`: outbound routes that apply to every extension carry it, routes that
  list source extensions do not (documented). Retries: 3 attempts, 5 minutes
  apart, only for calls that did not connect; a T.30 failure after connection
  is final (`failed` or `partial`).
- **Fax email.** The recipient is the destination's address, else the enabled
  users linked to the owner extension (stored as the sentinel `@owner` and
  resolved at send time). Six attempts with backoff; no recipient is a final
  `mail failed`. The attachment is `fax-<yyyymmdd-hhmm>-<id>.pdf`.
- **Storage and retention.** PDFs live in the `hello-recordings` bucket under
  `fax/<yyyy>/<id>.pdf`. `HELLO_FAX_RETENTION_DAYS` (default 90, 1 to 3650) is
  an environment setting, not a console one. A fax still `receiving` after two
  hours is closed as failed.
- **API.** `GET|POST /faxes`, `GET|DELETE /faxes/{id}`, `GET /faxes/{id}/pdf`
  as the spec says, plus `GET|POST /fax-destinations`, `GET|PUT|DELETE
/fax-destinations/{id}`, and `GET /me/faxes` with `GET /me/faxes/{id}/pdf` so
  a `user` role reaches the faxes of its own extensions. Viewers read the list;
  the PDF needs operator (a fax is a document); operators send and delete.
  `POST /faxes` takes multipart or JSON (`pdfBase64`, 700 KiB) so an agent can
  send; `GET .../pdf` is excluded from MCP as a binary download. A fax being
  sent is cancelled by `DELETE`; one being received answers 409.
- **CDR.** Kind `fax` (constraint widened in `00076`), the direction, the trunk
  and the route as for any call, and the fax result in the failure reason of a
  call that ends normally. The fax row carries the CDR correlation id; the CDR
  API gains no fax columns.
- **Not replicated.** Fax calls are not part of in-call HA: a node lost during
  a fax drops it (the sender retries; a received fax keeps its pages as
  `partial`).
- **Migration.** `00076_faxes.sql` (renumbered at merge; highest on main was `00060`): `faxes`,
  `fax_destinations`, `trunks.fax_mode`, and `fax` added to the inbound route,
  IVR key, IVR exit and CDR kind constraints. The migrate test counts are
  main's plus this one (49, 47 and 46). Other branches in flight take higher numbers; the merge
  captain renumbers.
- **Hardware.** Interop with a real carrier and a real fax machine (G.711 and
  T.38, ECM, a carrier-initiated re-INVITE, a 488) is the lab list in
  docs/fax.md. Everything else is tested: a generated multi-page fax goes
  through the modems over G.711 (PCMU and PCMA) and over T.38 UDPTL, from a PDF
  rasterised by pdftoppm to the received PDF, and a stopped session keeps its
  pages.

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

Built on #280 (choices the decision left open):

- **The write tool.** `applyConfigBatch` in the assistant (go-ai-sdk tool,
  `internal/ai/assistant/apply.go`). Its batch is validated exactly as a
  proposal's batch (inner allowlist, no credential property, at most 50 ops,
  `baseRevision` and `expect` from the graph, the dry run as the user), stored
  as a proposal (so the overlay, the proposal page and the session link show
  it), then applied with `proposal.DBStore.ApplyAsAgent`:
  `POST /api/v1/config/batch` replayed as the user under a new
  `auth.Agent.Apply` identity. That identity holds write and admin scopes with the user's own
  role, so a viewer's batch is `403 forbidden_role` and an operator's
  admin-only op is refused; `agentGuard` admits its writes only on the batch
  route and inside that batch (no direct object write, layout or revert). One
  applied batch per question; an answer may not add a proposal after it.
  Credentials stay out, as for every proposal.
- **Audit.** The actor is the user (`user:<name>`), the via `ai:<session>`
  (decision 2's form) on the `config_batches` row, on the proposal
  (`ai_proposals.applied_via`, new) and on the proposal's `apply_applied`
  audit row (now with its via); each object's row carries `batch:<id>` as for
  every batch. The proposal also keeps `batch_id` (new, set for a person's
  apply too) so the console reverts it with the ordinary
  `revertConfigBatch`. Migration `00086_ai_proposal_applied_via.sql`,
  additive.
- **The switch: per session, ask first by default (chosen).** Decision 2 lets
  the AI apply but sets no default, so the canvas panel's "Let AI apply
  changes" starts off each time the panel opens and holds for the questions
  sent while it stays open; each question stores it (`context.allowApply` in
  `ai_messages.context`), and only that question gets the write tool. Viewers
  see no switch. Not a stored per-user setting: no setting outlives the panel,
  so nothing applies without the user turning it on where they are looking.
- **UI.** An AI-applied batch is drawn as what was applied: the canvas
  reloads, places the new nodes and marks new and changed nodes; the panel
  shows "Applied by the assistant for you" with the operations and a
  Revert (dry run, then confirm). History and the proposal page label it
  "by the assistant for <user>".
- **No new API status codes.** The refusals are the existing
  `403 forbidden_role` and `403 insufficient_scope`.

## Integrations P6: HubSpot and the internal/crm framework (2026-11-10)

Decided while building, without asking, within the spec and the brief:

- **Scope of P6 as built.** Framework, HubSpot connector (OAuth, contact search, call
  engagements, owners, caller names), admin API, console page, MCP reads, the `hello-crm`
  skill and docs. The HubSpot calling iframe and its SDK controller (I-50) are NOT in this
  PR: its click-to-call needs the originate API and the first-party `hello-hubspot` OAuth
  client from P3 (#234), which are not on main. The server half of the handshake
  (`POST /api/v1/integrations/hubspot/engagements`) is built and tested so the iframe can
  land as a small follow-up once P3 merges.
- **Recording links** are behind a `crm.LinkMinter` interface (mint, revoke by label). The
  link service is P4's (#281, not merged), so `hello-control` passes none yet: a connector
  with recordings on logs calls without a link. Wiring P4's service in is one line in
  `cmd/hello-control/crm.go`. Links are minted with label `crm:<connectorId>`; disabling a
  connector, switching recordings off, disconnecting or deleting it revokes that label.
- **Caller-name budget.** The spec bounds a lookup to 1.5 s, but a call must never be held
  that long. The CRM query keeps the 1.5 s bound and runs in the background (it fills the
  Valkey cache); the SIP path waits at most 250 ms (`DefaultCallerNameBudget`). A repeat
  caller is a cache hit. Three failures in a row open a 30 s breaker per connector.
- **Carrier name wins** when it is a real name; a display name that is only a number is
  replaced by the CRM's. Numbers under 6 digits (extensions) are never looked up.
- **Ownership.** A call is owned by the HubSpot owner with the same email as the Hello user
  of the call's extension (exact, case-insensitive match; never display name). Hello's
  `users.email` is administrator-set, so it is treated as verified. No match: the
  connector's fallback owner, or the call is `skipped` and shows in the console with a
  retry. A manual mapping is never replaced by an automatic one.
- **One log per call per connector** (unique `(connector, callId)`): a missed call logged
  first is not logged again by the call's `call.completed`; a ring group's call is logged for
  the first member's event.
- **Late engagement handshake:** if the server already logged a call, its engagement is kept
  (`adopted: false`); the iframe's stays in HubSpot as a separate record.
- **Callback is a public route** (`GET /api/v1/integrations/crm/oauth/callback`): the
  cross-site redirect carries no Strict session cookie, so the single-use 10-minute state
  (stored as a hash, bound to the administrator) is the credential. It always redirects to
  `/integrations/crm`. No PKCE: HubSpot does not use it for confidential apps.
- **Routes beyond the spec table:** `DELETE .../{id}/connection` (disconnect),
  `GET .../{id}/users` and `PUT .../{id}/users/{userId}` (user mapping, the "field mapping"
  of the console), `GET .../oauth/callback`. The job list is scope `write`, role `operator`
  (the route test ties `read` to viewer; the spec wants operator).
- **MCP:** reads only (`listCRMConnectors`, `getCRMConnector`, `listCRMUsers`,
  `listCRMLogs`); everything that handles credentials or requeues is console-only.
- **Needs-reauth notice** is e-mailed to every administrator with an address, when SMTP is
  configured; otherwise it is the console banner only. "The owner" in the spec is read as
  the administrators, since a connection has no single owner.
- **Link renewal job** runs hourly on every replica (idempotent single UPDATE) instead of a
  daily job under an advisory lock.
- **HubSpot facts still to prove with a real account** (listed in docs/crm.md): the scope
  set (`crm.objects.contacts.write` for the Calls object), the phone search filters, the
  disposition ids lookup, and the EU host split.

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

## kw manifests for fax, coturn and the door camera allow-list (2026-11-13, deploy/kw-fax-coturn-door)

Draft PR "HOLD: kw manifests (deploy when unpaused)": merging to main deploys through Sync, and deploys are paused (registry 503), so it stays a draft until the operator resumes them.

- **hello-fax (#273)**: `kw/fax.yaml`, a Deployment and a ClusterIP Service on 8083, no Ingress. hello-sip-1/2 and hello-control get `HELLO_FAX_ENABLED`, `HELLO_FAX_ADDR=hello-fax.hello.svc.cluster.local:8083` and `HELLO_FAX_TOKEN`. The token is a new sops Secret `hello-fax` (`faxToken`, random), shared by the three callers and the engine.
- **coturn (#282)**: `kw/coturn.yaml`, `coturn-1` on master-11 (192.168.10.101) and `coturn-2` on master-12 (192.168.10.102), host-networked, Recreate, one replica each, UDP/TCP 3478, TLS 5349 and relay 49152-49999/UDP, the chart's own settings. TLS is a cert-manager Certificate `hello-coturn-tls` from cluster-ca with both addresses as IP SANs. The secret is a new sops Secret `hello-coturn` (`turnSecret`, random), read by both coturn pods and hello-control, so `hello-app` (whose private key would be needed to edit it) is untouched. hello-control gets `HELLO_TURN_URLS` for both servers (UDP, TCP, TLS) and `HELLO_TURN_SECRET`.
- **`TURN_ALLOWED_PEERS` is 192.168.10.101-192.168.10.108**, every node that can run a hello-sip (master-11 to master-13, worker-21 to worker-25): the two hello-sip Deployments are host-networked but not pinned, and their media address is the node's. Wider than today's two nodes; pinning hello-sip would narrow it. Earlier draft values (scratchpad coturn-values.yaml) were lost with the scratchpad, so these follow the chart's defaults and the cluster's node list.
- **Not enabled here:** web devices themselves. They need `clientAuthKey` (`HELLO_CLIENT_AUTH_KEY` on hello-control and hello-sip), which the brief did not ask for; coturn and the ICE-server route are deployed without it.
- **`HELLO_DOOR_CAMERA_ALLOW=192.168.10.0/24`** on hello-control (#285, still open: its config reads the variable and an older binary ignores it). 192.168.10.0/24 is the LAN of the nodes and the phones; narrow it if the cameras sit on a smaller network.
- **Image tags** of hello-fax and coturn are `sha-<the main commit these files were written at>`; ci.yaml publishes them on push to main. They must be re-pinned to the tags of the release commit when deploys resume (kw has no earlier pin for either).
- **Test:** `TestChartEnvParityWithKw` renders the chart with fax and coturn on so kw and chart agree; `HELLO_DOOR_CAMERA_ALLOW` is listed as kw-only (the chart sets it through `control.extraEnv`). `TestKwCoturn` and `TestKwFax` pin the manifests.
- **Validated** with `kubectl --context kw apply --dry-run=server` (the whole rendered set, server-side, field manager kuvryn-sync): every object accepted; the private render was deleted afterwards.

## kw: HELLO_CLIENT_AUTH_KEY for browser softphones (decided: add)

The user decided to add the web-device key to the held kw change (#334). It supersedes the earlier note that web devices stay off.

- A new sops Secret `hello-client-auth` (`clientAuthKey`, `openssl rand -base64 48`, generated straight into the encrypted file and never printed) is read as `HELLO_CLIENT_AUTH_KEY` by hello-control and by hello-sip-1 and hello-sip-2, the same key on all three, because hello-control issues the derived credentials and hello-sip checks them.
- `HELLO_CLIENT_AUTH_KEY_PREVIOUS` is not set; rotation will add it (docs/webrtc.md).
- Web devices still need the WSS edge to register (`HELLO_WSS_URL`), which is not part of this; the key and TURN servers are ready for it.
- #334 stays a HOLD draft; nothing here deploys until it is merged.

## Browsers on kw: TLS hop or cleartext hop (2026-11-14)

- #340 refuses a WebRTC (browser) call whose signalling reached hello-sip over
  the cleartext UDP hop from Kamailio, unless
  `HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP=true`. kw runs with WSS and the TLS
  hop off, so the web softphone (#343) cannot call there until one is chosen:
  turn on the TLS hop for kw (`KAMAILIO_HELLO_TLS_PORT`, hello-sip
  `HELLO_SIP_TLS_ADDR`, the Hello CA certificate for both) together with WSS,
  or turn on WSS and allow the cleartext hop (the hop is node-to-node on the
  kw LAN).
- The user chose WSS + the TLS hop (2026-11-14). #334 turns on both for kw,
  with a Hello-CA certificate for Kamailio and hello-sip.
- Implementation (#334, kw manifests; docs/webrtc.md, "Browsers on kw"):
  - Kamailio: `KAMAILIO_WSS_PORT=8088`, `KAMAILIO_WS_ORIGINS=https://hello.kw.watteel.lab`,
    `KAMAILIO_WSS_TRUSTED_PROXIES=^10[.]42[.]`, `KAMAILIO_HELLO_TLS_PORT=5073`,
    `KAMAILIO_HELLO_SIP_TLS_PORT=5061`; ports `wss` and `hello-tls`; dispatcher
    set 2 for hello-sip-1 and hello-sip-2; checksum annotation updated.
    kamailio.cfg, tls-hop.cfg and the kamailio image (kc-5f94d2b0d090316a
    already has the websocket and tls modules) are unchanged.
  - Trusted proxies: ingress-nginx is one pod in kw's pod network (not
    host-networked; Cilium tunnel mode), so the regular expression is the pod
    CIDR 10.42.0.0/16. If Kamailio sees a node address instead, X-Forwarded-For
    is not believed and every browser shares the ingress's throttle key (safe;
    checked after deploy, tracker hardware list).
  - Browser path: Service and Ingress `kamailio-ws`, `/sip` (Exact) on
    hello.kw.watteel.lab, the console's `hello-tls`, backend HTTPS, one-hour
    timeouts, no cluster-issuer annotation (the Ingress `hello` owns
    hello-tls). `HELLO_WSS_URL=wss://hello.kw.watteel.lab/sip` on hello-control.
  - hello-sip-1/-2: `HELLO_SIP_TLS_ADDR=:5061` and `_CERT/_KEY/_CA` at
    /etc/hello-sip-tls, port `sip-tls` (also on the headless Services),
    `fsGroup: 65532`, and WebRTC media on (`HELLO_MEDIA_WEBRTC_ENABLED`, port
    21100, `ALLOW_CLEARTEXT_HOP=false`): without it the anchor would refuse
    browser legs.
  - Certificates from the existing `cluster-ca` ClusterIssuer (the Hello CA
    phones and the console's browsers already trust; its Secrets carry
    ca.crt), no new issuer. `hello-edge-tls` (Kamailio, on every TLS socket as
    tls-hop.cfg requires, so WSS and the hop share it): IP SANs
    192.168.10.101, .102 and the VIP .109, the two DNS names, server and
    client auth. `hello-sip-node-tls` (both nodes): hello-sip-N,
    hello-sip-N.hello.svc and hello-sip-N.hello.svc.cluster.local, server and
    client auth. It replaces `hello-sip-tls` (server auth, .101 only) under a
    new name, and Kamailio's volume is no longer optional, so a new Kamailio
    pod waits for the new certificate instead of racing cert-manager's
    reissue of the old Secret. The go-live patch is regenerated for it.
  - Chart bug fixed: the hello-sip StatefulSet mounted its TLS Secret 0440
    with no fsGroup, so the image's user 65532 could not read the key; it now
    gets `fsGroup: 65532` with `sip.tls.enabled` (TestChartTLSHop).
  - Tests: TestKwBrowsers; TestChartEnvParityWithKw renders WSS, the hop and
    WebRTC media.
  - Renewal: Kamailio reads its certificate at start, so it must restart
    within 30 days of a renewal (docs/tls.md); hello-sip re-reads its own.
  - hello-fax and coturn are re-pinned to sha-cb5b6da6f7a705e381227870d3133ea566382e42
    (main's #343 merge, whose publish built both); the other images keep
    main's pins.

## TLS hop from Kamailio to hello-sip (2026-11-13)

- S-12 is opt-in at both ends: `KAMAILIO_HELLO_TLS_PORT` on Kamailio and
  `HELLO_SIP_TLS_ADDR` on hello-sip (chart `kamailio.helloTLS`, `sip.tls`). Off,
  kamailio.cfg and hello-sip behave as before; kw does not set either, so its
  ConfigMap only gains the unused `tls-hop.cfg` key and the new defines.
- It carries every encrypted phone leg (tls, ws, wss), not only WebSocket, so
  it is testable in the lab with the existing TLS phone. (A first draft kept
  one dispatcher set and converted the chosen destination to its TLS address;
  it was replaced by the second set below, probed over TLS from hello_tls.)
- Hello's requests come back to `hello_tls` on the instance's own address (as
  `hello_ws` does for WSS), mutual TLS with one certificate of the Hello CA.
  Kamailio verifies the node's certificate only on connections it opens
  (`tls-hop.cfg` client section); what arrives on hello_tls is vetted by source
  address like the UDP socket, because the tls module's server section cannot be
  set per socket from a static file.
- hello-sip needs the edge to present a client certificate (RequireAndVerify).
  The certificate is re-read when it changes (cert-manager renewals).
- The dispatcher has a second set (the same nodes by TLS address, `sockname`
  attributes naming hello_tls) probed over TLS; an encrypted leg is balanced
  over it, so a node whose TLS listener is down is skipped for those legs only.
  The lab proves it by stopping a node and checking set 2 follows. hello-sip
  counts `hello_sip_requests_by_wire_total{transport}` so the lab can assert a
  REGISTER arrived on the TLS listener; Kamailio's `edge_requests_total` shows
  Hello's requests on hello_tls.
- The cleartext-hop refusal is built here, after #324 merged: a WebRTC leg
  whose signalling did not arrive over the TLS hop is refused unless
  `HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP` is set (internal/sip/tlshop.go).
- Merged with SIP over WSS (#326), both opt-in and independent. With the hop
  on, every encrypted leg (tls, ws, wss) goes over `hello_tls` and dispatcher
  set 2: the hop supersedes `hello_ws` for WSS legs (both are this instance's
  own address, and the hop is the encrypted one). With the hop off, WSS keeps
  #326's behaviour (`hello_ws` under EDGE_VIP, else `hello`). Requests arriving
  on `hello`, `hello_legacy`, `hello_ws` and `hello_tls` are all vetted against
  the dispatcher and flagged as from Hello. NEED_TLS is defined by
  KAMAILIO_TLS_PORT, KAMAILIO_WSS_PORT or KAMAILIO_HELLO_TLS_PORT; the tls
  module reads `tls-hop.cfg` with the hop on, else `tls.cfg`. Because
  `tls-hop.cfg` presents one certificate on every TLS socket, the chart refuses
  `kamailio.helloTLS.enabled` with `kamailio.wss.enabled` when
  `kamailio.wss.secretName` names a different Secret than
  `kamailio.tls.secretName`. kw keeps both off.

## Messaging 4, LAN chat widget: defaults chosen (2026-11-11)

Built from `.procoder/specs/messaging-channels.md` M-28, M-29, M-29a to M-29c.
Where the spec left a product decision open, the conservative option was chosen
and is listed here as chosen by default; change one by editing this entry and
the code it names.

- **Routing.** A chat conversation goes to the unassigned inbox pool, which is
  what `route_kind = 'inbox'` is today. Queue routing and ACD assignment are
  item 5, so the "default route (queue)" site setting is not offered yet.
- **Who is available.** Agent staffing (M-13) is not on main, so a site is
  online when it is inside its opening hours, a supervisor (operator or admin)
  has an inbox stream open (seen in the last two minutes, `inbox_presence`), and
  the unassigned live chats are under the site's cap. Otherwise the widget goes
  offline: it collects a name, an email address and a message, and the chat is
  flagged offline.
- **Opening hours.** The M-14 schedule and the time-conditions engine are not
  on main, so a site keeps its own weekly schedule (an IANA zone and windows per
  weekday); empty means always open. A bad schedule counts as open, so a typo
  never silences the widget.
- **Agents are anonymous to visitors.** A visitor sees "Support", never an
  agent's username or display name.
- **Visitor events** are in `chat_events`, not `conversation_events`: that
  table is per user and its user id is a required key. Same design (ids in
  commit order, 7 days, `Last-Event-ID` resume on any pod).
- **A challenge endpoint.** `GET /chat/v1/challenge?site=` returns the site's
  public look and the nonce to solve; the spec lists the session call but a
  proof of work needs its nonce first. Nonces are signed, expire in three
  minutes, are bound to the client address and the site, are accepted once per
  pod, and may never carry less difficulty than the site's setting.
- **Turnstile and the frame's policy.** Turnstile cannot render under
  `script-src 'self'`, so only a Turnstile site's frame also allows
  `https://challenges.cloudflare.com` for scripts and frames. Every other site
  keeps the spec's policy exactly. The admin page warns that Turnstile needs
  the browser and the server to reach Cloudflare.
- **Blocks.** A block holds the visitor for the hours chosen (24 by default, at
  most 720) and the day's address hash until 00:00 UTC at most. A blocked
  visitor gets a normal 200 with a closed state and a worthless token; nothing is
  stored. Unblocking is on the sites page.
- **Daily address salt.** The hash is HMAC-SHA256 under a key derived from
  `HELLO_SECRET_KEY` and the UTC date, so there is no salt table and the salt
  rotates at 00:00 UTC on every pod.
- **One stream per visitor** is enforced per pod (a second stream is refused
  with 429 after waiting up to a second for the old one to close, so a page
  reload is not refused); there is no cross-pod stream registry.
- **Adaptive difficulty** is tracked per pod in memory (this minute against
  three times the median of the last hour, at least 10 sessions): only a
  hardening, nothing depends on it.
- **Email replies** are allowed only for chats the visitor left while nobody was
  available and gave an address for; a live chat is answered in the widget. The
  email holds the reply and no link, and the failure is kept as a failed message.
- **A resolved chat reopens** when the same visitor writes again within
  `reopen_days` (M-12); after a visitor ends a chat and returns later, a new
  conversation starts. A chat an agent marked spam stays closed to that visitor.
- **A site collecting nothing still takes an email address while offline**, so
  the reply has somewhere to go.
- **Defaults.** The listener is off until `HELLO_MSG_CHAT_LAN_ADDR` is set; the
  global switch `HELLO_MSG_CHAT_ENABLED` defaults to true; a new site has the
  proof-of-work challenge at 16 bits, no contact collection, no transcripts,
  links kept, and no allowed origins until some are listed (the frame is then
  not embeddable by any page).
- **Not built, and why.** Chat attachments (`POST /chat/v1/attachments`, the
  site's attachment switch) and the site logo from MinIO are not built: the
  media pipeline and its tables (M-18) are not on main, and the spec has
  attachments off by default. The route does not exist (404), so nothing is
  half-wired. They join the media work.

## Integrations P8: directory engine and AD/LDAP sync (2026-11-10)

Built from spec `integrations.md` I-1 to I-10 and I-12 to I-19 (range
auto-provisioning I-11 stays P12; the Google, Entra, SCIM and OIDC connectors
build on the same `Driver` and `Store` later). The spec left the points below
open; each was **chosen by default (the conservative option)**, not by the
user.

- **Scheduling starts only after a manual apply.** A connector never changes
  users on its first, unreviewed run: the scheduler skips a connector until one
  `apply` run (a reviewed preview applied, or "Run now") has succeeded. New
  connectors are created disabled.
- **Mass-deprovision threshold.** "More than 20 percent of linked users
  (minimum 5)" is read as: the run halts when it would disable more than
  `max(5, 20 percent of the enabled linked users)`. A small install (under 25
  users) is therefore only guarded by the empty-result guard below 6
  disables. The empty-result guard is `needs_confirmation` with code
  `empty_result` (not a hard failure), so an administrator can confirm a
  genuine emptying.
- **A created directory user has no usable password and no new login method.**
  The spec's SSO-only rule for synced users needs the later SSO phase. In P8
  the sync stores the marker `!` as the password hash (it never matches, "an
  empty or unknown hash never matches") and keeps `auth_method = 'local'`: the
  `users_auth_method_check` constraint is left alone, so the later phase adds its
  own value. A local user an administrator _adopts_ keeps its password, so
  adopting never locks anyone out; the later phase flips adopted users.
- **Adoption needs a decision.** Matching is by email, then username (a
  username match whose email differs is the `username_collision` conflict).
  Adopt rows are applied only when listed in the apply body (`adopt` /
  `adoptAll`); otherwise they stay `adopt_not_confirmed`. A local admin is
  skipped (`local_admin_not_adopted`).
- **Only `extension_linked` can be overridden**, by moving the extension from
  its current user, on the administrator's explicit decision (audited). Every
  other conflict reason is not overridable.
- **An empty or missing extension value never unlinks.** A user whose mapped
  extension is empty keeps the link it has; one with no extension value is
  created without a link (not a conflict). Auto-create is P12.
- **Disabling is allowed on a managed user, enabling is not.** An
  administrator may disable a directory user by hand (the safe direction);
  re-enabling, and every other managed field, is `409 managed_by_directory`.
  A directory user has no local password: setting one is refused too.
- **Deleting a connector with linked users is refused** (`409
connector_in_use`) unless `release=true`, which turns the users into local
  users (keeping their rows, no password) and the ring-group rows into manual
  ones.
- **Reads are admin, not operator.** The spec lists runs and changes as
  "operator"; the route table ties role to scope (a read scope is viewer), and
  runs name users, so every directory route is scope `admin`, role admin. The
  MCP tools are the five read operations; the writes, the test, the sync and
  the apply and confirm are excluded (credentials, human-in-the-loop).
- **Incremental runs (I-12).** Only a scheduled run reads incrementally; a
  preview, a manual apply and a confirm always read everything, so an apply
  matches its preview. AD reads `uSNChanged>=` the `highestCommittedUSN` taken
  from the rootDSE before the read, kept per connector with the server's
  invocation ID; without a USN, `whenChanged>=` (AD) or `modifyTimestamp>=`
  (other LDAP) from the read's start less 5 minutes for clock skew. The mark is
  stored only after a run applied with no error row. A full run is forced when
  there is no mark, 24 h after the last full run, when the config, mappings,
  group rules or mode changed (a fingerprint), when the invocation ID changed
  or the USN or clock went backwards, and when a group the rules use changed
  (AD does not bump a user's USN for a membership change). An incremental run
  never counts, skips or disables a user for being absent and never trips the
  empty-result guard; only full runs detect absences. Unchanged users with a
  conflict are re-examined by the next full run, not before. Stored in
  `directory_connectors` (`sync_mark`, `sync_fingerprint`, `last_full_run_at`)
  and shown per run (`incremental`, `fullReason`). `delta_token` stays reserved
  for Entra.
- **Stable id.** AD `objectGUID` in its canonical string form, else
  `entryUUID`, else `nsUniqueId`; an entry with none fails the run
  (`bad_filter`) instead of being skipped, because a skipped user would read as
  absent and be disabled.
- **LDAP servers must be reachable through the SSRF-safe dialer**: public, or in
  `HELLO_INTEGRATIONS_ALLOWED_CIDRS` (RFC 1918 by default). Loopback and
  link-local never are.
- **A failed connection test is a 200** with `ok: false` and a classified
  code, so the console can show it; a failed run is a run row, not an API
  error.
- Devices a sync switches off are marked (`devices.disabled_by_directory`) so
  that only those come back when the user does. Applying is per user, in its
  own transaction under the configuration lock when it touches ring groups or
  devices, so one bad row becomes an `error` row and the run goes on.
- Live tests against a real Active Directory (certificate chains, paging at
  1000, nested groups, `userAccountControl` flags) are on the PR's
  hardware-and-accounts list; the suite uses an in-process LDAP server.

## Messaging item 3: desk-phone texting and the SIP trunk channel (2026-11-10)

The SIP-trunk adapter, the inbox gateway hand-off (`InboundFromTrunk`), replies
through the trunk and the `messages.conversation_message_id` link already
shipped with the inbox core (#211). This item adds what was missing. Where the
spec left a product decision open, the conservative option below was chosen by
default; each is a one-line change if the user decides otherwise.

- Part size stays 700 bytes (the sip-messaging queue limit, which also keeps a
  part in one UDP datagram), not the spec's 1000. Desk-phone parts include the
  `(1/3) ` counter and any `[WhatsApp] ` prefix inside the 700. A body from a
  phone above 700 bytes is refused with 413 as sip-messaging already does,
  below RFC 3428's 1300.
- Delivery to the desk phone happens when a conversation is assigned or taken
  (the latest customer text) and on each later customer text while it is
  assigned. Resolving, snoozing or returning it to the pool sends nothing.
- A phone never makes first contact: a number with no conversation is
  `handled=false` and takes the old external-number path, so no conversation is
  opened from a handset. Spec M-17 consent gating therefore needs no phone path.
- A customer who is in another agent's open conversation is refused to a phone
  (with the reason sent to it), never texted around the inbox, and so are
  opted-out customers. A resolved conversation of the user's own is not
  reachable from a phone unless the number's owner extension is theirs.
- A phone's reply is rate limited per user by the text queue's
  `rate_per_minute`, counted on inbox replies; the refusal is 429.
- `owner_extension_id` lets the owner's phone reply only into an existing
  conversation that is unassigned or its user's. It does not create one.
- Desk-phone delivery is off for the number and for the user by default and
  needs both. A text that cannot be queued for the phone (text messaging off, a
  door station, the text queue's rate limit) is skipped silently: the inbox
  always keeps the text.
- Vendor capability `sip_message` is on for Grandstream only. Every other
  vendor stays off until a lab check shows its handset has a text UI. An
  extension with no phone yet counts as capable, so an administrator can set it
  up first.
- Contact naming overlays the stored name at read time with the reader's own
  books (personal, named, company, extensions), contact first, then the
  carrier's name. Only a shared source is ever stored on the conversation. Add
  to contacts defaults to the user's own book; the company book is chosen
  explicitly by an operator or administrator. The click-to-call button and the
  peer's recent CDRs in the right panel of spec M-15 wait for the integrations
  click-to-call and the CDR number filter.
- Chat visitor alerts ("New chat in Support") are not built: there is no chat
  channel yet (item 4).

## Browser extension (integrations P5, 2026-11-13)

- The extension is plain JavaScript modules under `web/extension/`, with no
  bundler, so the source loads unpacked as it is. It is packaged by
  `web/extension/tools/build.mjs` (a deterministic zip, and a CRX3 signed with
  `node:crypto`, no dependencies) in the web image build, and served by nginx
  from `/downloads/`. The download card is on the Dial page (user role), not a
  new nav item.
- Fixed id: the manifest carries a fixed public key, so the extension id is
  `kengpjalagamnmmkljldjhhgonmbdcph` everywhere, and the preregistered client
  `hello-extension` has the single redirect `https://<id>.chromiumapp.org/`
  (not derived from the deployment URL, since it cannot vary). A Go test and a
  web test recompute the id from the manifest.
- Signing key: the private key is never committed. The image build reads it from
  the `extension_key` build secret (CI repo secret `HELLO_EXTENSION_KEY`); with
  none, only the zip is built and the card says "unsigned". Docker's layer cache
  does not key on a secret, so the first build without it must not be reused
  once the secret exists (bump the cache or rebuild).
- `GrantableScopes(user)` now includes `calls`, `events` and `recordings`, as
  spec I-31 says. Without it a `user` could not grant the extension its scopes.
  The handlers already limit a user to their own extensions (originate, events,
  recording links), so no new access is opened; `docs/ai-access.md` and the
  role tests are updated.
- Tokens are kept in `chrome.storage.local` (the refresh token must survive a
  browser restart). Refreshes are serialised, since the refresh token rotates and
  reuse revokes the grant.
- The page script is registered dynamically only for sites the user lists and
  the browser has granted (`optional_host_permissions`), so there is no
  `<all_urls>` grant and no always-on content script.
- The incoming-call notification looks the caller up through
  `GET /api/v1/lookup/number` (I-43, #304); see "Extension caller lookup" below.

## Integrations P17: Slack, n8n and Make (2026-11-13)

- **Slack is its own small queue, not a webhook endpoint format.** The webhook
  dispatcher signs Hello's own JSON and counts health per endpoint; Slack needs a
  different body, no signature, an `Authorization` header for a bot and an
  answer to parse (`{"ok":false,"error":...}` comes with HTTP 200). Bending
  `webhook_endpoints` for that would touch P2 code other builders and P7 use, so
  `internal/slack` has its own two tables (`slack_destinations`, `slack_posts`,
  migration 00089) and reuses the exported pieces of P2: `webhook.RetryDelay`
  (the same eight-attempt schedule) and the retry and dead-letter rules.
- **Posts are queued in the event ingest transaction** (a `TxHook`, like webhooks
  and the CRM), with the event stored in the row, so a post never waits on
  `integration_events` retention and a repeated event posts once
  (`UNIQUE (destination_id, event_id)`).
- **Both Slack credentials are supported** (spec table): an incoming webhook URL
  (host pinned to `hooks.slack.com` / `hooks.slack-gov.com`, https, path
  `/services/`) and a bot token with a channel (`xoxb-` only, fixed API address).
  The secret is write-only and sealed under the AAD `slack:<id>`; the MCP
  decision is exclude, category credentials.
- **Slack failures decide the destination's state.** `invalid_auth`,
  `token_revoked`, 401 and 403 switch the destination off (`auth_failed`), a 404
  or 410 from a webhook switches it off (`destination_gone`), 50 failures in a
  row too; a new credential switches it back on. Channel errors
  (`not_in_channel`, `channel_not_found`) only kill the post. Names and numbers
  of callers are untrusted: `<`, `>`, `&` and mrkdwn characters are neutralised
  (no `<!channel>` or links from a caller name) and each field is cut to 80
  characters.
- **`voicemail.received` is a new event type** (spec: "voicemail posted to a
  channel" needed one). hello-sip emits it after the message row is stored, once
  per box for a voicemail group, with the caller, the box extension and
  `durationMs`; never the audio. An endpoint that subscribes to every event now
  receives it too (the envelope is additive).
- **n8n and Make reuse the generic surface, no new routes.** The trigger
  subscribes through `POST /webhooks` and deletes through `DELETE`; the actions
  are originate, `cdrs` and the lookup route. The n8n trigger verifies
  `Hello-Signature` itself (Hello's signing secret is returned once at
  subscription and kept in the workflow's static data; a subscription that
  already existed has no secret, so it is replaced). Make cannot compute an HMAC
  in IML, so its hook URL is the secret, as decided for Zapier (P7). The Find
  Contact operation uses `GET /api/v1/lookup/number`, which comes with P7 (#304):
  it is not duplicated here, so those two operations work once #304 is merged.
- **No build step for n8n.** The node is plain CommonJS (no TypeScript, no
  `n8n-workflow` dependency) so `node --test` runs it in CI with a fake n8n
  context and the tests read `openapi.json` to prove every request is a
  documented route.

## Integrations P18: Outlook and Google contacts, calendar DND (2026-11-13)

- **Per-user connectors in the existing framework.** `crm_connections` already
  had `scope = 'user'`; the framework now uses it: `Capabilities.UserScope`,
  `Repo.{UserConnection,SaveUserConnection,DeleteUserConnection,UserConnections,UserConnectionsOf}`,
  `TokenManager.ConnFor` and `RefreshFor`. Per-user tokens are sealed under
  `crm:<connector>:user:<user>:<access|refresh>` so a blob copied to another user's
  row does not open. The administrator creates one connector (the customer's own
  Microsoft or Google app) and each user connects; the org-level connect and test
  refuse a per-user connector. A re-authorisation need of a personal connection
  is shown to that user, not e-mailed to the administrators.
- **The OAuth state carries who started it** (`user:<id>:<actor>` in the existing
  actor column, no schema change), so the shared callback finishes a personal
  sign-in for the right user and redirects to `/me/connections`; a state of the
  wrong scope is refused.
- **Caller names from the called user's contacts, after the organisation's
  CRM.** `Lookup.NameFor(extension, number)` resolves the extension to its user
  (cached 60 s) and asks only that user's connected connectors; hello-sip tries
  it when the route sends the call straight to an extension (`dec.Extension`),
  within what is left of the same 250 ms budget. A ring group, queue or IVR is
  named from nobody's contacts: choosing whose address book would be a guess.
  The carrier's real name still wins. Cache keys are per user.
- **Contacts are matched locally on an in-memory index.** Graph has no phone
  search on contacts and People's `searchContacts` is prefix-based and a
  warm-up-dependent, so the connectors read the address book (20 pages of up to
  1000, 20,000 contacts) into `crm.ContactIndex` kept 10 minutes per account. The
  key is the **last eight digits** (nine would miss the trunk `0`: `050 12 34 56`
  vs `+32 50 12 34 56`); two contacts with one key are ambiguous and name nobody.
  The read outlives the 1.5 s lookup (`WithoutCancel`, one minute) so the first
  call after a quiet spell misses and the next ones hit.
- **Features choose the scopes.** Per-user connectors take `features`
  (`contacts`, `calendar`); the sign-in asks only for what is on. Google asks for
  `calendar.freebusy` only, never event content; Outlook asks `Calendars.Read`
  but selects only `showAs`, `isCancelled`, `isAllDay`, `start`, `end` and
  `responseStatus`.
- **Calendar DND is polled every two minutes** by one replica (`TryLock`), eight
  calendars at a time. Change notifications are not used (public callback plus
  renewal for a 2-minute gain). `extensions.dnd_source` (`manual`, `calendar`,
  migration 00090) records who set DND; the two manual writers (extension update
  and the feature-code path) set it to `manual` whenever they set `dnd`, the
  poller writes `calendar` only where DND was off and clears only `calendar`
  ones, in a config transaction (audit, revision bump, hello-sip reload) that is
  skipped when nothing would change. A user who turns DND on by hand during a
  meeting therefore takes it over; one who turns it off is not fought until the
  next stretch. With several connected calendars DND stays while any is busy; an
  unreadable calendar keeps it until the last known end plus 5 minutes.
- **`dndSource` joins the extension in the API** (`Extension`, `SelfExtension`),
  so the console and agents can tell a calendar DND from a manual one.
- **Pre-existing behaviour changed:** `ActiveConnectors` now means "has any
  non-revoked connection" (org or user) instead of "has an org connection", so a
  per-user connector is listed once someone connects; the org lookup and the
  logging hook skip it (`Capabilities.UserScope`, `LogCalls` false).

## Integrations P7: Zapier private integration (2026-11-10)

Built per spec `integrations.md` I-52 and the P7 phase line. Choices the spec left
open; each was chosen by default (the conservative option), not by the user.

- **`GET /api/v1/lookup/number` (I-43) is built here.** The Find Contact search
  needs it, and neither main nor the P4 PR (#281, which carries only the call
  and recording lookups) has it. It is a thin wrapper over the existing
  caller-name lookup (extensions, then company, no callee) and adds the
  credential's own personal contacts only when nothing there names the number.
  Chosen by default: no `extension`/`callee` parameter (the named books of a
  phone group need a callee; a connector has none), the personal book is the
  last resort (so the answer for a number does not depend on who asks when the
  company already names it), and a service account never reads a personal book.
  Scope `read`, role `user` (listed as self-service in `TestRoutesTable`).
  `PhonebookLookup.source` gains `personal`.
- **Webhooks API unchanged.** I-42 already made `POST /api/v1/webhooks`
  idempotent on `(url, events)` and `DELETE` by id, which is all a Zapier REST
  hook needs. Subscribing therefore keeps the existing requirement: an
  administrator credential with the `secrets` scope. Chosen by default over
  letting a user subscribe to their own events; widening webhook access to
  non-admins is a separate product decision. Actions and searches need no admin.
- **Zapier does not store the signing secret and Hello's signature is not checked
  on this route.** Zapier hook URLs are unguessable and cannot verify an HMAC;
  the docs say so.
- **Find Call has no server-side number filter.** `GET /cdrs` has no number
  parameter, and adding one is not in the P7 text. The search takes a CDR ID
  (exact, `GET /cdrs/{id}`) or a number, matched in the app against the latest
  200 calls (caller, called and original destination, as written or on the last
  nine digits). `lookup/calls` of P4 is not used because it is unmerged.
- **Place Call input**: destination, optional extension, optional idempotency
  key; nothing else (no caller ID, no auto-answer), so the originate limits and
  the emergency refusal are the only policy.
- **The app is plain JavaScript with no installed dependencies** and its tests
  run with `node --test` against a fake Zapier runtime (CI job `zapier`). The
  `zapier-platform-core` version in `package.json` is the current release at the
  time of writing and is only needed by `zapier push`. Not tested here: a real
  Zapier account (invite, a Zap run end to end).

## 2026-11-13: per-line Grandstream keys (feat/prov-perline-keys)

- Lines 2 and up now get SRTP mode and key exchange, presence, RTCP-XR collector and backup server keys, at the account offsets found in the official templates (wp8x6 1.0.3.39, wp810_822_825 1.0.11.83, wp820 1.0.7.90, wp856 1.0.3.16).
- Correction to #262: the WP820 template does have a Secondary SIP Server, at P50602 (account 1) and P51102 (account 2); line 1 now writes it.
- Send Anonymous (P421, P521, ...) for further lines stays unwritten, as decided before.

## HubSpot calling iframe and recording links (2026-11-13, feat/hubspot-iframe)

Completes the part of P6 (#289) that waited for the originate API and the first-party clients (decisions of 2026-11-10).

- **The iframe is a console route**, `/integrations/hubspot/iframe`, built on HubSpot's own `@hubspot/calling-extensions-sdk` (0.9.7) rather than a hand-written postMessage protocol. The SDK is loaded on demand, so the console bundle does not carry it.
- **First-party client `hello-hubspot`** (public, PKCE, redirect `/integrations/hubspot/callback`, scopes `calls events`), registered with the dialer at start-up. Sign-in is a popup, because a framed page gets no Strict session cookie. The popup hands the code to the iframe with `postMessage` to its own origin; the state value and the PKCE verifier stay in the iframe. Tokens live in the iframe's localStorage (HubSpot's frame gives it partitioned storage); a refused refresh drops them.
- **The click-to-call id is HubSpot's call id.** `externalCallId` is the originate id; events carry `originateId` and `callId`, so the first event of the call gives the SIP call id, which goes with the engagement id to the existing handshake route. An inbound call on a user's extension uses its SIP call id.
- **Recording links in engagements need no new code in the connector:** HubSpot already writes `hs_call_recording_url` and the worker patches the adopted engagement; a test now covers it. The link store is the one #328 wires in `cmd/hello-control/crmlinks.go`; this change wires no second minter.
- **Framing.** nginx answers the iframe route with `frame-ancestors https://app.hubspot.com https://*.hubspot.com` and every other console page with `frame-ancestors 'none'` and `X-Frame-Options: DENY` (spec T-12; only the consent page had it). A deploy test checks the template.
- **Not done, needs a HubSpot account:** whether `incomingCall` reaches an unlisted app, the exact calling-settings fields (the connector panel shows name, url, width, height, isReady, supportsCustomObjects), and the framing hosts per region (all are `*.hubspot.com`). They are on the lab checklist in docs/crm.md.

## Integrations P14, P15, P21: Pipedrive, Zoho CRM, Odoo, Bitrix24 (2026-11-13)

Decided while building, without asking, within the spec and the brief:

- **One migration, 00087.** `crm_call_logs.state TEXT NOT NULL DEFAULT ''`
  (additive). The connector settings live in the existing `config` JSON and `kind` has no CHECK.
  The migrate counts in `test/integration/migrate_{prov,ai,ai_agent}_test.go` are main's plus one.
- **Sign-in follows #328's names.** Odoo (API key) and Bitrix24 (inbound webhook) are `crm.Direct`
  connectors (`auth` is `direct`; Connect verifies the stored secret and returns an empty
  `authorizeUrl`; a refused one is a field error on `clientSecret`) and use `crm.SecretBound`
  (Zoho: `region`; Odoo: `url`, `db`; Bitrix24: none, its webhook address is the secret), the
  shared `internal/crm/kit` helper and `crm.ErrNoTarget` (Odoo, a call with neither a contact
  nor an owner). Odoo's access token is the API key, Bitrix24's the webhook address.
- **Multi-step writes survive a restart.** A connector returns `crm.PartialError{State, Err}`
  from `LogCall` when it fails after a step that cannot be repeated; the worker saves State on
  the job and hands it back as `CallLog.State` on the retry, clearing it on success. Bitrix24
  uses it for register then finish then attachRecord, so a retry (also after a `hello-control`
  restart) never registers the call twice. This replaces an in-memory record.
- **Pipedrive: one org-level connection, not one per user.** The spec's table says tokens are
  per user "for attribution"; the call log's `user_id` gives the same attribution from one
  administrator's connection, so per-user sign-in (and its console flow) is not built. The
  user is matched by email (`users/find`), manual mapping as for every connector. Pipedrive's
  `api_domain` is kept as the connection account and used as the REST base only when it is an
  https `*.pipedrive.com` host.
- **Pipedrive recording is an upload.** `Capabilities.RecordingUpload`: the worker keeps the
  call log's reference first (status `update`), then opens the audio from MinIO through
  `crm.AudioSource` (wired in `cmd/hello-control/crmaudio.go`) and the connector uploads it
  once. A failed upload is retried as an update, never as a second create. Over
  `HELLO_CRM_UPLOAD_MAX_MB` (default 50) or a deleted recording: the call stays logged and the
  job's `lastError` says why. No link is minted, so nothing is renewed.
- **Bitrix24 copies its recording.** `Capabilities.RecordingCopy`: a link of at most one day
  (`CopyLinkTTL`) is minted, Bitrix24 fetches it once, and `link_expires_at` stays empty so
  the renewal job never re-attaches a duplicate file.
- **Zoho** sends the recording link in the call's description, as the spec says; a renewal
  rewrites the description. Leads are looked up after contacts and attached as `Who_Id`.
  Zoho's refused-code answer is HTTP 200 with an `error` member and is treated as permanent.
  PhoneBridge click-to-call is not built (marketplace only, decision 1).
- **Odoo logs a note in the chatter** (spec: "chatter/log, verify"): no call model is shared
  by all editions. Unknown caller: the note goes to the call owner's own partner so the call
  is not lost. It speaks `/jsonrpc` (Odoo 14 to 19); `/json/2` is on the lab list, since
  `/jsonrpc` is deprecated for removal in Odoo 20. `author_id` is not set (the note is the API
  user's), so the later body update is always allowed; the owner's name is in the body.
- **Bitrix24 register is not idempotent** on the Bitrix side; see the state above. No lead is
  created for an unknown number (`CRM_CREATE = 0`), `SHOW = 0` (no screen-pop in the portal).
- **Private-network CRMs.** The CRM HTTP client now allows `HELLO_INTEGRATIONS_ALLOWED_CIDRS`
  (RFC 1918 by default) in `hello-control` and, via a new config field, `hello-sip`, so a
  self-hosted Odoo or Bitrix24 is reachable. Loopback, link-local and metadata stay blocked.
- **Recording links** come from #328's `cmd/hello-control/crmlinks.go` minter (no second
  wiring here); `newCRM` gains only the audio source for Pipedrive's upload.
- **MCP:** unchanged. The four read tools list the new kinds; the `hello-crm` skill and its
  failures table were taught the new kinds.

## Integrations P16 and P20: helpdesks, PSA tools and Clio (2026-11-13)

Decided while building, without asking, within the spec and the brief:

- **Direct sign-in is a framework concept.** Zendesk, Freshdesk, ConnectWise, Autotask and
  HaloPSA have no browser consent (spec: customer API token / org-level API member). A
  connector implements `crm.Direct` (`Authenticate`) instead of `crm.OAuth`; the existing
  `Connect` call signs in with the stored credentials, proves them with a harmless read and
  stores the result as the org connection. A static credential has no expiry; HaloPSA's
  client-credentials token has one and is requested again by the TokenManager. The API
  `auth` field says `oauth` or `direct`. No migration: the secret is the connector's sealed
  client secret and the other settings are `config`.
- **Secret-bound settings.** A setting that decides where the secret is sent (subdomain,
  host, company, region, identity) asks for the secret again when it changes
  (`crm.SecretBound`; default `clientId` only). A new secret on a connected direct
  connector is proved before it replaces the stored one.
- **`crm.ErrNoTarget` means skipped, not dead.** ConnectWise, Autotask and Clio need a
  contact (and Halo a client) to put a call on; a call from an unknown number is `skipped`
  with the reason and can be retried. Zendesk creates an end user for an unknown caller
  (setting `createContacts`, default on, international numbers only); Freshdesk lets
  Freshdesk create the contact from the phone.
- **One ticket per call for helpdesks** (Zendesk Talk's behaviour), the first Zendesk comment
  private so the customer is never e-mailed. The recording link is a second private
  comment/note, added only if the ticket does not already carry it (a read first), so a
  retry or a renewal never repeats it. PSA/Clio records are rewritten whole (idempotent).
- **Record choice for the "verify each" cells of the spec:** ConnectWise = Sales activity of
  type Call; Autotask = company note with the contact; Clio = PhoneCommunication; HaloPSA =
  ticket. Phone search is by the last four digits (or the exact E.164 for Zendesk and
  Freshdesk) followed by a digit comparison in Hello, because each system stores numbers as
  typed. Contact ids that need two values are compound (`contact:company`, `Person:id`,
  `user:client`).
- **Sidebar apps do not hold a Hello credential.** The Call button opens `/dial?n=` in a new
  tab (the agent is signed in there). So the "pop from the SSE stream" inside the helpdesk is
  not shipped; the pop stays in `/dial` and the events stream. The Zendesk app is a ZAF 2
  zip; the Freshdesk app is an FDK 2.3 source package (`fdk pack` then upload). Both are
  built per request by `GET /integrations/crm/{id}/app` with Hello's https address embedded
  as an escaped JSON string; Zendesk Talk Partner Edition stays out of scope, as in the spec.
- **Recording links are now wired** (`cmd/hello-control/crmlinks.go`): the P6 follow-up that
  P6 left as "one line". Links carry `recording_links.connector_id` (reserved by P4), so
  revoking a connector's links is one UPDATE. No migration, so no placeholder number is
  used and the migrate counts are unchanged.
- **Clio revoke on disconnect is not done:** Clio's deauthorize takes the access token, the
  framework's `Revoker` passes the refresh token; the administrator revokes in Clio.

## Clients item 1: media foundation on pion (2026-11-13)

Spec: `clients-webrtc-mobile-video.md` S-2 to S-6. Built on main without
waiting for #276 (DTLS-SRTP, ICE-lite per leg). Choices:

- **The SDP model wraps pion/sdp and keeps every attribute in order.** A parse
  then build is a no-op, tested over a corpus of vendor and browser SDPs
  (`testdata/sdp`). `ParseAudioSDP` is not re-implemented on it: the spec says
  "where trivial", it is not, and #276 changes that code. `AudioSection`
  bridges a section to it instead.
- **The shared port is a mux that routes by ufrag, then by source address.** It
  does not authenticate: the leg's ICE-lite agent does. Addresses per leg are
  capped at 8 so a forged check cannot grow the table. This replaces pion/ice's
  UDP mux, which assumes a full agent per connection.
- **Relay legs read a `LegConn`** (a UDP socket or a mux endpoint) so the relay,
  recorder and announcements are untouched.
- **The thin seam to #276** is three calls after it merges: bind the leg's
  `Secure` to the endpoint (`Bind(ep.LocalAddr(), ep.WriteToUDP, ...)`), call
  `ep.SetUfrag` with the ufrag its `iceLocal` made, and pass `Fingerprint()` and
  the answered setup into `WebRTCNode.Local`. The mux and SDP code use no #276
  names (`muxIsSTUN`, `fingerprintUsable`), so the two merge without clashes.
  `TestWebRTCToPhoneAudio`, `TestDTLSFingerprintMismatch` and the plaintext
  check need #276's handshake and follow with that glue.
- Video is rejected in the answer; accepting it is item 6.

## Clients item 1 glue: WebRTC legs keyed by DTLS-SRTP (2026-11-13)

Follow-up to "Clients item 1: media foundation on pion". Choices:

- **`Relay.AnswerWebRTC` is the one join.** It validates the offer on the model,
  routes the leg's ICE-lite ufrag to its mux endpoint before the answer exists,
  lets the leg's `Secure` accept the DTLS offer (the handshake, fingerprint
  pinning and keys are #276's code, unchanged) and writes the answer on the
  model with the fingerprint and role Secure chose. A leg that cannot be keyed is
  refused, never answered in the clear (`SecurePolicy{Require: true}`).
- **`AddMuxLeg` binds the leg's `Secure` exactly as `AddLeg` does**, so the
  relay, recorder and announcements see no difference between a socket and the
  shared port.
- **`ParseAudioSDP` is not unified on the SDP model.** The model (pion/sdp) needs
  v=, o=, s= and t= lines; phones and trunks send SDPs without them and call
  setup must answer them, so the strict reader cannot replace the tolerant one.
  `TestParseAudioSDPStaysTolerant` pins the difference. `AudioSection` remains the
  bridge.
- **Tests:** `TestWebRTCToPhoneAudio` (PCMU and Opus; Opus is off in `-race`
  builds, as everywhere), `TestDTLSFingerprintMismatch`, with a pion DTLS-SRTP
  client as the browser. They check that nothing but STUN and DTLS crosses the
  browser leg before the keys are in, that nothing on it is in the clear after,
  and that a cleartext packet from the browser is not relayed.

## Clients items 5 and 6: web softphone and web video (2026-11-13)

Builds on #324, #276, #341 and the devices and ICE servers of #282. Choices:

- **The softphone core lives in `web/src/softphone/`, not in a `clients/*`
  workspace package.** The spec names a pnpm workspace so the mobile app can
  share the core. A workspace would move the `hello-ui` image build context from
  `web/` to the repository root, which touches the image build in CI and the kw
  manifests for no gain while the apps are "not now". The seam is kept instead:
  everything that touches JsSIP is in `jssipEngine.ts`; the rest uses the
  `PhoneEngine` and `Platform` interfaces in `types.ts` and has no DOM or React
  in it (`phoneState.ts`, `quality.ts`, `backoff.ts`, `lock.ts`), so moving the
  folder into a workspace package when the apps start is a mechanical change.
- **No Playwright run in this change.** The spec asks for a Chromium test through
  the lab compose stack; that needs a browser and the lab, so it is on the
  hardware list. In its place: a headless pion DTLS-SRTP client places calls
  through the whole SIP path (`internal/sip/webrtc_test.go`) and the relay
  (`internal/media/webrtcvideo_test.go`), and `vitest` covers the state machine,
  the Web Lock rule, the failure states and the dock with a fake engine.
- **Message waiting uses the SUBSCRIBE the server already answers.** JsSIP's
  `Subscriber` sends `Event: message-summary`; no REST poll.
- **A browser is a leg by its SDP, a browser callee by its binding.** An INVITE is
  a browser's when its audio section is `UDP/TLS/RTP/SAVPF` with ICE; a contact is
  a browser when it registered over WS or WSS or its device is a web or mobile
  client (`isWebBinding`). This works with or without the WSS edge's `wss`
  transport value (#326), so this change does not depend on it.
- **Mixed rings need a leg of each kind: the alternate callee leg.** One relay
  has two legs, and a ring across a desk phone and a browser has two kinds of
  callee. Instead of a relay per fork, a third endpoint (parked until a fork
  answers) is offered to every browser fork and promoted by the winner. All
  browser forks of one call share its ICE credentials and fingerprint; only the
  one that answers sends checks, and a simultaneous second answer is hung up by
  the existing first-answer-wins rule.
- **Every SDP for a browser leg carries its video line.** `AudioSDP.Build` appends
  the video section (or the rejected one that keeps the dialog's `m=` count) for a
  browser leg, so transfer, park, pickup and hold code that builds SDP the old way
  cannot drop an `m=` line, which a browser refuses. `SetTarget` ignores browser
  legs for the same reason: their address is what ICE verified.
- **The cleartext-hop gate is real.** A WebRTC offer is refused with 488 unless the
  edge-to-node hop is TLS or `HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP` is on. Until
  #340 (the TLS hop) deploys, kw sets the flag, as the spec says.
- **Browser calls are not replicated.** The ICE and DTLS state is the node's own;
  a taker cannot be given it. A dead node ends the call (the browser's ICE failure
  hangs up in about five seconds) and a new call works at once. Re-homing by ICE
  restart (S-7) is a lab item. This is stated in `docs/ha.md`.
- **Video is relayed, per leg pair, never decoded.** Constant sequence and
  timestamp offsets (so NACKs map by subtraction), one SSRC per leg, payload type
  per leg, RTCP terminated and regenerated with PLI, FIR, NACK and REMB carried
  across, a token-bucket ceiling at `HELLO_VIDEO_MAX_KBPS`, a node limit
  `HELLO_VIDEO_MAX_SESSIONS`. RTX, RED, FEC, transport-cc and header extensions
  are not negotiated. Video is for a web client on both legs; every other callee
  (desk phone, trunk, IVR) gets `m=video 0`. The existing door-station video
  relay (plain RTP) is untouched and is skipped for a browser caller.
- **Mid-call video is accepted optimistically on the browser that asks.** The
  re-INVITE is answered before the peer is asked (the existing hold path answers
  first), so the peer is offered the one codec the browser listed first and, if it
  refuses, the video ends again and the trace says so. The cost is a moment of
  one-way video in that case; the benefit is that hold, resume and video changes
  share one path.
- **`extensions.video_allowed` and `cdrs.video`, and a client-quality table
  matched to the CDR like RTCP-XR.** `POST /me/calls/{callId}/quality` files a
  report under the dialog's SIP Call-ID and the device (it must be the caller's
  own); the CDR detail reads reports whose Call-ID is the CDR's `sip_call_id` or
  `peer_call_id`, so a report that arrives before the CDR is not lost. Retention
  90 days, like the phones' reports. The route is `session`-scoped like the other
  device routes; the scope `softphone` of the spec is introduced with the mobile
  OAuth work.
- **Out of this change, by the plan:** video recording (6b: a recorded video call
  records audio only and the trace says so), conference-room video (11), video
  phones and door stations (10).

## WebRTC offers need the TLS hop (2026-11-13)

- `media.Relay.AnswerWebRTC` takes a `SignalingHop`; an offer that arrived over a
  cleartext edge-to-node hop is refused with `ErrCleartextHop` unless
  `HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP=true` (default false). The check runs
  first, before the leg is routed or a handshake starts.
- hello-sip decides "encrypted" from the transport sipgo read the message on
  (its TLS listener) from a trusted edge, never from the Via the sender wrote;
  `wireLabel` (and so `hello_sip_requests_by_wire_total`) uses the same source.
  `Server.checkWebRTCHop` logs the refusal at warn with the fix and counts
  `hello_sip_webrtc_refused_total{reason="cleartext_hop"}`; the softphone call
  path (not built yet) answers 488.

## Integrations P13 and P19: Salesforce Open CTI and Dynamics 365 CIF (2026-11-13)

Decided while building, without asking, within the spec and the brief:

- **Two halves per CRM.** The interactive half is a static page Hello serves
  (`web/public/integrations/{salesforce,dynamics}`), served by the UI's nginx with a
  per-page Content-Security-Policy (`frame-ancestors` only the CRM's hosts, no inline
  script, `connect-src 'self'`); the server half is a `internal/crm` connector. The
  pages need no CRM token: Open CTI and CIF run in the user's own CRM session.
- **No Hello cookie in the frame.** A third-party frame does not get Hello's session
  cookie, so the pages sign in through a popup (authorization code with PKCE) against two
  new pre-registered public clients, `hello-salesforce` and `hello-dynamics` (spec I-32),
  with scopes `calls events`; the callback pages postMessage the code to their own opener
  (origin and state checked) and the token stays in the frame's session storage.
- **Client credentials use the framework's `crm.Direct`** (introduced by #328). `Authenticate`
  signs in with the customer's client id and secret and no browser. `Connect` connects at once
  (empty `authorizeUrl`), and the token manager signs in again at expiry or on a 401 instead of
  refreshing. Refused credentials are the same `needs_reauth` once as a refused refresh token;
  an outage is not. Salesforce and Dynamics both use it (spec: connected app / org service
  principal). JWT bearer for Salesforce is not built: client credentials is the simpler flow
  Salesforce offers for an integration user and needs no certificate handling.
- **Salesforce interactive-only mode.** `config.serverLogging` (default `false`): with it
  off the connector is only the softphone's settings. It needs no credentials, the hook
  queues no job, the lookup skips it and the engagement handshake is a no-op
  (`adopted: true`), so nothing waits for a connection that will never exist. `crm.ServerSide`
  is the framework switch for it. The softphone then also logs missed calls for its user
  (`?missed=1` in the adapter URL the call center definition carries).
- **No duplicate Tasks.** The softphone saves the Task at answer (`saveLog`), then tells the
  server (`POST /api/v1/integrations/salesforce/engagements`, `callId` and `recordId`);
  the server patches that Task with the duration, outcome and recording link. A server
  Task that exists first wins (`adopted: false`). This is the HubSpot handshake
  generalised: `Service.Engagement` validates the record id through the optional
  `crm.RefValidator` of the connector.
- **Dynamics logs only on the server.** The CIF widget does click-to-act and the screen pop;
  it does not write activities, so there is no Dynamics handshake route.
- **Dynamics channel provider is entered, not imported.** The spec says "customer-imported
  solution". A CIF v2 channel provider is a data record (Customer Service admin center,
  Channel Providers), not a solution component, so Hello shows the exact values to enter
  (`GET /api/v1/integrations/crm/{id}/setup`, also an MCP read `getCRMSetup`) instead of
  generating a solution zip nobody could verify here. The channel URL carries
  `?org=<environment URL>` so the page knows where to load the CIF library from; it loads
  that library only from `https://*.dynamics.com`. The same endpoint gives Salesforce its
  Call Center definition XML and adapter URL.
- **Dynamics phone call states.** Created open, then closed (Made for outbound, Received for
  inbound; a missed call is Received with "Missed call from" and "No answer", Dynamics has
  no missed reason). A closed activity is read-only, so an update reopens, patches and
  closes. A failed close after a successful create is retried once and never repeats the
  create. Sovereign clouds (`.us`, `.cn`) are not supported: only `*.dynamics.com`.
- **Phone matching.** Salesforce: SOSL `IN PHONE FIELDS` on the last nine digits. Dynamics:
  Dataverse has no normalised phone column, so the lookup queries a bounded list of
  spellings (the last eight digits run together, and grouped with a space, dash, dot or
  slash) with `contains`, and the server verifies the digits of each candidate (last eight,
  which national and international spellings share). The widget's screen pop uses the same
  list unverified. Mixed or unusual separators can still be missed (documented).
- **Shared HTTP client is `kit.Do`** (#328's kit), which gained an optional `Classify`
  (the CRM's own error message, and a retry for a failure such as Salesforce's 403
  `REQUEST_LIMIT_EXCEEDED`). `crm.RecordingLine` (replace-not-repeat of the `Recording:`
  description line, never touching what a person wrote) stays Salesforce and Dynamics
  specific. HubSpot keeps its own client; it is not touched.
- **Frames' lint.** `web/public/integrations` are classic scripts without a build and are
  excluded from eslint; `web/src/integrations/embedded.test.ts` runs them against fakes.
- **No migration.** The connector kind column has no CHECK; the placeholder 00102 is
  unused.

## One 802.1X model for Wi-Fi and wired ports (2026-11-13, feat/prov-wifi-8021x)

Reconciles the Wi-Fi profiles (#278, migration 00074) with the 802.1X and SCEP profiles (#279, migration 00076). Migration 00088.

- **An enterprise Wi-Fi profile names an 802.1X profile** (`wifi_profiles.dot1x_id`, `dot1xProfileId` in the API, `ON DELETE RESTRICT`). The EAP method, identity, anonymous identity, password and the RADIUS CA live in the 802.1X profile only. The Wi-Fi profile keeps SSID, security, PSK, priority, hidden, preferred account and the verified mark. The API fields `eapMethod`, `identity`, `anonymousIdentity`, `password`, `caCert` and `passwordSet` are gone (a request carrying them is refused as an unknown field).
- **Old columns stay, unread** (`eap_method`, `identity`, `anonymous_identity`, `password_enc`, `ca_cert`), because the repo's migrations are additive for rolling upgrades; a later migration drops them.
- **Existing enterprise profiles are adopted by hello-control at start-up** (`Store.AdoptWifiEAP`), not in SQL, because the sealed password has to be re-sealed under the 802.1X profile's context (`dot1x:<id>`, read from `wifi-password:<id>`). Each becomes an 802.1X PEAP profile "Wi-Fi <name> #<id>". A profile whose identity has spaces or exceeds 64 characters (the 802.1X rule) stays unadopted, is logged and writes no network until an 802.1X profile is named.
- **Server certificate check is the 802.1X profile's:** the system certificates, or its uploaded CA (P83016 = 3, CA in P83011) with the optional server name (P83018 = 1, P83019). "Trust on first use" (2) and "skip verification" (0) are no longer offered; an adopted profile that had either becomes the system certificates. Reason: #279 decided Hello never writes 0, and one check per profile is what a shared model means.
- **EAP-TLS on Wi-Fi** is now possible (P83004 = 1, P83007 CA, P83009 identity) where the phone has a SCEP certificate; the client certificate (P83008) is not written, as P8440 is not on the wired port (verify on a handset). EAP-MD5 is refused for a Wi-Fi profile (the API, and a 802.1X profile in use cannot be changed to it); a network whose 802.1X profile is missing, EAP-MD5 or EAP-TLS without SCEP is skipped at render, and its slot goes to the next profile.
- **Template re-check of #278** against config-template-10212026.zip: WP8x6 slots 1 to 10 at P83000 + 100 (n - 1) (SSID +0, security +2, key +3, EAP +4, priority +49, preferred account +85), security codes 0, 4, 5, 11, 12, 14, EAP codes 1 TLS and 2 PEAP, CA type 0 skip / 1 system / 2 TOFU / 3 uploaded, phase 2 0 MSCHAPv2, and the network-5 EAP key at P83304: all confirmed. WP810/822/825: SSID +0, enabled +1, security +2, key +3, 802.11r +48, hidden +50 per slot for 17 slots; slots 18 to 20 have security at +72: confirmed. WP856: P7812, P7814 (0, 1 WEP, 2 WPA/WPA2 PSK; the template calls it "Security Mode for Hidden SSID"), P7830: confirmed. Radio keys P25438, P82332, P22232, P22231, P82334 (WP8x6), P84992 (WP810), P25420, P25421, P22443, P22230, P25315, P82333 (WP856): confirmed. No mismatch found. Not written because #278 did not know them: P83018 and P83019 (domain validation, now written), P83086 and P83087 (management frame protection and transition mode), P83051 and P83052 (IP mode, IPv4 mode).

## WP lab sweep runs against a third control replica (2026-11-13)

- `TestWPPhonesAsVendors` (S-38) needs the server-wide provisioning settings
  that are environment variables (TCP models, TLS models and server, backup
  registrar, RTCP-XR) rendered into WP files, without changing what the
  other lab tests' phones get. The compose lab therefore gains
  `hello-control-wp` (host 8444) with those settings; phones are bound through
  the API on hello-control-1 and fetch their files from it.
- The WP822 is provisioned for TCP (with a backup server), the WP836 for TLS
  with SRTP required, because a model has one transport; between them every
  transport key is read back. The emulated phones register over UDP direct to
  hello-sip-1, as the park test does; TLS and TCP signalling stay covered by
  the TLS lab test and the unit tests.
- 802.1X on a WP is the Wi-Fi enterprise (PEAP) profile on the WP836; wired
  802.1X is GRP26xx only, so the sweep asserts a WP file carries no P7901.
- The hardware lab list is printed at the end as "confirmed" (Hello's side
  proven by a subtest of the run) or "needs hardware".

## Messaging item 2: outbound sending on Twilio (2026-11-10)

Delivery item 2 of `.procoder/specs/messaging-channels.md`, on the inbox core
(item 1, #211). Migration `00091_messaging_twilio.sql`. Everything additive: no existing CHECK constraint
changes, so `pg_temp.hello_check_values` is not needed.

What is in: `internal/messaging/provider` (the interface and registry; only
what works on the LAN: `Validate`, `Send`, `Status`, `Numbers`) and its Twilio
adapter; the send, status-poll and settle workers (`messaging.Worker`, run on
every control replica); sealed credentials (`secret.MsgChannelAAD`, the same
`secret.Box` as trunk passwords); first contact (`POST /api/v1/inbox/conversations`,
settings `firstContact` and two limits); the per-number throughput cap; the
numbers route; the Inbox "Text a customer" dialog, the Twilio fields of the
channel dialog and the first-contact settings.

How inbound replies arrive without N1: **they do not.** A Twilio number cannot
receive a text while there is no public listener (spec Default 6), and polling
for inbound (`Reconcile`, N3) is not scheduled. So a Twilio conversation starts
with a first-contact text and only the outbound side lives in the inbox. This
is documented in `docs/inbox.md` and the channel dialog.

Choices made by default (the spec leaves them open; the conservative option was
taken):

1. **First contact is a supervisor action only** (`POST /api/v1/inbox/conversations`,
   operator and above, scope `messages`); the `user` role has no route to start
   one (no `/me/inbox` form). Fewer people can send to a customer who has not
   texted. The Inbox status carries `senders` for supervisors only.
2. **The first-contact guard applies to every new outbound conversation**, even
   to a number that texted before in a resolved conversation. A customer with
   an **open** conversation on the number is `409 conversation_exists` (reply
   there) rather than reused.
3. **First contact only through a carrier number**; a SIP trunk number is
   `409 channel_unavailable` (its texts have no web API, and the spec names
   provider numbers as the case).
4. **No idempotency key on first contact:** a double submit hits the open
   conversation (`409`). One message, one recipient, one attestation.
5. **A consent note is mandatory** (1-500 characters) besides the attestation
   flag. It is stored on the conversation (`consent_note`); the audit row holds
   ids only ("first_contact attested"), never the note or the number.
6. **Credentials are replaced as a whole** (SID and token together, optionally
   the Messaging Service SID), after Twilio accepts them. The token cannot be
   changed alone, so a stored SID is never paired with an unchecked token.
7. **Save is refused when the carrier is unreachable** (`502`), not stored
   unvalidated: an administrator who cannot reach Twilio cannot add a channel.
8. **A lost or 5xx answer is `unknown`, never retried.** A 429 from Twilio is a
   definite refusal (`failed`, `rate_limited`). Failures that are not the
   customer's (lost answers, 429) count toward `degraded` at five in a row; a
   bad number (21211 and the like) does not.
9. **Queued for an hour without a send becomes `failed` (`not_sent`)**, since it
   never reached the carrier, rather than `unknown`.
10. **Throughput cap default 60 texts a minute per number**, enforced when a
    text is claimed (one advisory lock), so the cap holds across replicas.
11. **Limits default as in the spec:** 20 first-contact texts per agent per
    hour, 200 per number per 24 hours; the `429` carries `Retry-After: 600`.
12. **Status polling schedule:** 5 s doubling to 5 min for the first hour, then
    30 min, stopping at the final state, on `404`, after 48 hours, or while the
    channel's credentials are invalid. No `StatusCallback` is sent (LAN-only).
13. **Twilio's `21610` on send or on a status read** records an opt-out with
    source `provider`, as if the customer had texted STOP.
14. **Body limit 1600 characters** for a carrier text (Twilio's), on top of the
    API's 2000 bytes; longer is `400` on `body`. Media is never sent (MMS is N1).
15. **Erasing a customer does not call Twilio** (no provider-side `Redact`; that
    is item 8). The report already says the carrier keeps its own logs.
16. **MCP:** every new operation is excluded, category `private-data` (the
    messaging configuration and message content are never exposed to an agent),
    so no skill teaches them and the `docs/ai-access.md` doctable is unchanged.

Not testable without a Twilio account (hardware/accounts list in the PR): a real
send and delivery receipt, error code mapping against live Twilio, 10DLC and
toll-free registration state, Messaging Service sends, what carriers do to an
unregistered sender.

## Extension caller lookup (2026-11-13, feat/extension-lookup)

- **No new scope.** The lookup route needs `read` and the `hello-extension`
  client already asks for `calls events read`; the preregistered client and the
  scope rules are unchanged.
- **The route gains the fields the notification needs.** It returned only a
  name, so it now also returns `company` (the matching book entry's company)
  and `crm`: `{id, name, company, url}` from the CRM connectors (organisation
  connectors first, then the caller's own Outlook/Google), via
  `crm.Service.FindContact` and the 1.5 s budget and 5 min/60 s cache of the
  existing CRM lookup. `crm` is independent of `result`: a number can be `none`
  in the books and known to the CRM. A service account gets only organisation
  connectors. Additive; no other consumer changes.
- **Name precedence:** CRM contact, then an address-book match, then the name in
  the event. An ambiguous or unresolvable answer names nobody.
- **Quiet failure:** 404, no match, errors and a 2 s timeout all fall back to the
  event name. Cache in the worker's memory (5 min hit, 60 s miss and 404, 10 s
  failure); it is not persisted, and sign-out clears it.
- **CRM link** is an "Open in CRM" notification button (only http and https
  links), not a click on the body, which still just dismisses.

## Messaging item 5: routing and ACD (2026-11-11)

Built on origin/main so that routing does not depend on which channel created
the conversation: the router places any conversation whose `routed_at` is NULL,
so the Twilio (item 2), desk-phone (item 3) and chat (item 4) channels are
routed once they merge, with no change of their own. Where the spec left a
product choice open, the conservative one was taken and is listed here as
chosen by default; change one by editing this entry and the code it names.

- **Wake-up.** The router sweeps every 2 s on every control replica instead of
  waiting on `pg_notify` plus a 10 s tick (the repo has no LISTEN
  infrastructure, and the sweep is a few indexed queries). Returning
  unanswered conversations and releasing orphans run every 10 s
  (`router.DefaultSlowEvery`). Agent login lives in Valkey, which PostgreSQL
  cannot wake on either way.
- **Routing config is a sub-resource.** `GET|PUT /api/v1/messaging/addresses/{id}/routing`
  carries the route, schedule, after-hours action and first-contact text,
  rather than more fields on the address PUT, so the address contract of the
  other open messaging PRs stays as it is. `route_target` is the id of the
  queue, user, extension or ring group as text; a queue target must take texts
  when it is set.
- **Fallback is the supervisors' pool.** A route whose target is deleted,
  disabled, unlinked or (for a queue) off for texts sends the conversation to
  the pool. The spec's "the address's fallback route" is read as that pool.
  Turning a queue's texting off moves its unassigned conversations to the pool;
  assigned ones stay with their agent.
- **After-hours.** `none` routes as usual, `reply` sends the text and then
  routes as usual, `route` goes to the other target. Evaluated by the number's
  own weekly schedule (the time-conditions engine is not used for texts); an
  empty schedule is always open and an invalid stored one reads as open. The
  reply is sent once per conversation, never to an opted-out customer, and only
  a SIP trunk channel can send it today: another provider gets a system line
  saying the text was not sent, until its channel adds a send path.
- **First contact.** The first-contact text (M-14) goes once to a peer with no
  earlier conversation on that number, not together with the closed-hours
  reply, never to an opted-out peer.
- **Capacity.** An agent's open conversations are those assigned to them that
  are open or pending (snoozed does not count), across every queue and direct
  assignment. Only the router enforces it; a person who claims or replies is
  not blocked.
- **Statistics for the strategies.** Calls and last end come from
  `msg_agent_stats` in PostgreSQL (answered = conversations the queue gave the
  user; last end = when it last did), the round robin counter from
  `msg_queue_rr`, both separate from the voice statistics. A user linked to
  several member extensions counts once, by their best extension.
- **Reassign.** Only conversations the router assigned (the latest assignment
  row is `acd`) and only while open with the customer's last text unanswered
  by any user reply. The agent who let it lapse is not offered it again; with
  nobody else eligible it waits in the queue (its members can claim it). A
  conversation a person assigned, took or answered, or a direct user or
  extension route, is never taken back. An agent whose extension is unlinked
  or who leaves the queue loses their router-assigned conversations to the
  queue.
- **Shared pool visibility.** The members of a text queue and the users of a
  ring group see the unassigned conversations waiting there (and are told by
  the live stream and, with an address, by email); nobody else does.
- **Availability is the call queues' state.** One login and one pause per
  extension, so pausing for texts pauses calls too; the inbox sets it on every
  extension of the user that is a member of a text queue (`/me/inbox/agent`).
  With Valkey unreadable the router assigns to static members only.
- **Not offered.** The `ai_triage` route kind waits for the AI item; `desk_phone_delivery`
  and the owner extension wait for item 3 (the router calls `Store.afterAssign`,
  where that item's delivery on assignment belongs).
- **Deleting a queue** is refused (409) while a number routes to it or an open
  conversation is placed in it.

## Messaging item 6: Telnyx and sender registration (2026-11-13)

1. **Telnyx is one more adapter on the framework,** `internal/messaging/provider/telnyx`,
   registered by `cmd/hello-control` like Twilio. Credentials are an API key and
   an optional messaging profile ID (UUID), added to `provider.Config` as
   `APIKey` and `MessagingProfileID`; `Secret()`, `Hint()` and `Pooled()` replace
   the Twilio-only reads in the store. No migration: `msg_channels.provider` is
   free text and the sealed blob is JSON, so the placeholder 00130 is unused.
2. **Credentials are replaced as a whole,** as for Twilio: `apiKey` and
   `messagingProfileId` together after Telnyx accepts the key (a `PUT` with only
   `apiKey` drops the profile). The channel's `accountHint` is the last four
   characters of the profile ID (never of the key); empty without a profile.
3. **Validation lists the account's messaging numbers**
   (`GET /phone_numbers/messaging`), paged to 20 pages of 250, so `Validate` and
   `Numbers` are the same call and a key without messaging access is refused.
4. **Telnyx's 403 is split by code before status.** Error `40300` ("blocked due
   to STOP") is mapped to Hello's `CodeOptedOut` (the same constant Twilio's
   21610 uses), on send and on a status read, and so records a provider opt-out.
   Any other 403 (or 401) is refused credentials. 40300 is from Telnyx's docs,
   not a live account (on the accounts list).
5. **Status polling uses a 48 hour window for Telnyx too,** inside Telnyx's 10
   day limit, so M-9a's "never poll past 10 days" holds by construction.
   `delivery_unconfirmed` is shown as `sent` (it is carrier silence, not a
   failure); `sending_failed` and `delivery_failed` are `failed`.
6. **Registration is an optional `provider.Registrar`** with
   `Registration(ctx, numbers)`, not the spec's per-number `Registration(ctx, n)`:
   one call per carrier reads the brands, campaigns and verifications once and
   answers for every number, instead of N reads. Both adapters implement it.
7. **Registration states are the same five for every carrier** (`approved`,
   `pending`, `rejected`, `unregistered`, `unknown`) and the kind comes from the
   number (+1 toll-free prefixes, other +1, rest of the world). A number outside
   the US and Canada is `other`/`unknown`: this follows the spec's out-of-scope
   line (no country-specific registration, no UK/EU alphanumeric sender IDs, no
   India DLT). Sender IDs are therefore not read or managed.
8. **A section the carrier will not answer degrades, it does not fail.** The
   route names it under `unavailable` and its numbers are `unknown`; only when
   nothing at all can be read is it the carrier's error (`422` refused, `502`
   unreachable). A 403 on a registration read does not mark the channel
   `credentials_invalid` (the send path alone decides that).
9. **Twilio reads:** `BrandRegistrations` (brands), each Messaging Service's
   `UsAppToPerson` (campaigns, at most 20 services) and `PhoneNumbers` (which
   numbers are on a service), and `Tollfree/Verifications` keyed by the number's
   SID (`Number.ID`). A long code is approved only when it sits in a service whose
   campaign is verified. Twilio's `messaging.twilio.com/v1` root is a second API
   root in the adapter (`WithMessagingURL`, tests only).
10. **Telnyx reads:** `/10dlc/brand`, `/10dlc/campaign`,
    `/10dlc/phone_number_campaigns` and
    `/messaging_tollfree/verification/requests`. Field names are from Telnyx's
    published API and have not been checked on a live account.
11. **The checklist is derived server-side** (`buildChecklist`) so the console and
    any other client agree: brand, campaign, numbers on a campaign, toll-free,
    sample messages (always `manual`: Hello cannot read what a carrier reviews)
    and an answer to HELP (Hello's own `helpText`, done when every enabled number
    has one). Steps that do not apply to the account's numbers are
    `not_applicable`. Links point at each carrier's documentation; the Telnyx
    toll-free link is the messaging docs root because no stable page was verified.
12. **The numbers table loads registrations on its own,** one best-effort read per
    enabled carrier channel when the page opens (not for a SIP trunk channel or a
    channel with refused credentials); a failure leaves "Unknown". Nothing is
    cached or stored server-side.
13. **MCP:** the registration route is excluded like the other messaging routes
    (`private-data`); the doctable is unchanged.

Not testable without accounts (accounts list in the PR): a real Telnyx send,
delivery receipt and the 40300 STOP block; the exact fields of Telnyx's 10DLC and
toll-free reads and Twilio's `UsAppToPerson`/`Tollfree` reads; what a limited key
returns (403 vs 404) for each registration read; a Twilio account with several
Messaging Services.

## WSS on Kamailio and the SBC-peer trunk profile (2026-11-13)

- WSS (clients item 2) is opt-in by `KAMAILIO_WSS_PORT`, off in the chart
  (`kamailio.wss.enabled`). Kamailio ends TLS and the WebSocket on a `wss`
  listener on its own address; the Ingress (`/sip`, `backend-protocol: HTTPS`)
  is a separate chart object. Origins are an exact allow-list
  (`KAMAILIO_WS_ORIGINS`, default the console origin); a handshake without an
  `Origin` is let in because a browser always sends one and the client still
  needs digest. Unset list means no origin.
- The browser's real address comes from `X-Forwarded-For` only when the TCP
  peer matches `KAMAILIO_WSS_TRUSTED_PROXIES`, kept per connection id in an
  htable and sent as `X-Hello-Client`. pike is skipped for WebSocket clients
  because the ingress address is shared.
- hello-sip records `ws` and `wss` as transports. A `sips:` call may ring `wss`
  (encrypted to the edge) but not `ws`; SDES SRTP is still decided by TLS
  only, since a browser negotiates DTLS-SRTP.
- With the VIP, WebSocket clients use a separate `hello_ws` socket on the
  instance's own address (UDP 5072), so Path and Record-Route name the
  instance that holds the connection. HA activation is not changed.
- The Kamailio to hello-sip TLS hop (S-12) is not built: it needs the DTLS-SRTP
  work (#276) that this change does not depend on. It is a precondition for
  accepting WebRTC calls in production, not for registering over WSS.
- Teams (items 13 and P22): a trunk `profile` column (`generic`, `sbc-peer`,
  migration 00096), defaults on create, limits on create and PATCH, a profile
  list endpoint and MCP tool, a UI select, and docs/teams.md. No Teams code in
  internal/sip. Hello has no TLS or SRTP transport towards trunks, so the
  profile cannot ask for them and refuses `srtp: required`; the SBC encrypts
  towards Microsoft and the Hello to SBC leg is UDP on the LAN or DMZ. A TLS
  trunk transport and Diversion/History-Info are separate features that are
  not built.
- deploy/kuvryn-sync/kw/ is untouched by instruction, so its copy of
  kamailio.cfg lags: TestKwKamailioConfigMatchesSource fails until the
  manifests step copies deploy/kamailio/kamailio.cfg into the ConfigMap and
  updates the checksum annotation.

## Clients items 6b, 10 and 11: video recording, video phones and door stations, conference-room video (2026-11-13)

Stacked on #343 (items 5 and 6); merges after it. Choices:

- **A plain RTP video leg is the browser's leg with its own sockets.** A phone or
  a door station that offers `m=video` on RTP/AVP gets an even RTP port and the
  next one for RTCP from the RTP range, read by two goroutines per leg that feed
  the relay's existing `forwardVideo` and `handleRTCP`. So it has the same
  translator (offsets, one SSRC and payload type per leg, per-leg RTCP, PLI, FIR,
  NACK, REMB, the ceiling) and every pairing works: phone to phone, door station to
  phone, phone to browser, browser to phone. The old door-only "relay the port pair
  unchanged" (`VideoRelay`) is gone: it could not carry a door's video to a
  browser, could not enforce a slot or `video_allowed`, and could not be recorded.
  Its test now asserts the translated stream and the regenerated PLI.
- **The SDP section is written by the leg**, appended in `AudioSDP.Build` through a
  `localVideo` hook (every SDP of the dialog carries it, so transfer, park, hold
  and pickup cannot drop an `m=` line) and again by `withVideo` on the paths that
  skip the relay's SDP functions. An offer that had a video line is always answered
  with one, `m=video 0` unless carried; an offer without one clears it.
- **Plain video never rides beside encrypted audio and never reaches a trunk.** A
  plain video line has no SRTP here (`ParseVideoSDP` accepts RTP/AVP and AVPF only);
  sending clear video next to SRTP audio would defeat the policy, so the line is
  refused with the reason in the trace. Trunks and URI destinations stay audio only,
  with one exception below.
- **Codec match.** Between two plain ends any H.264 with the same packetization mode
  and `profile_idc` goes (the answerer's fmtp is passed back as sent, the relay does
  not look inside); with a browser on either end both must be Constrained Baseline
  mode 1, the one thing browsers decode. VP8 is never offered to a plain leg.
- **A phone that lists no `rtcp-fb` is still sent PLI and FIR** (the relay marks
  both on for a plain leg with none listed). Door stations and video phones answer
  a picture loss indication whether or not their SDP lists it, and a viewer who joins
  a door camera needs a keyframe now, not at the next GOP; NACK and REMB stay as
  negotiated.
- **Video recording (6b) is fragments in the bucket, a row per stream, a finalizer
  in hello-control.** The recorder reads the relay's `OnVideo` tap, depacketizes
  (H.264 per RFC 6184, VP8 per RFC 7741) and writes fMP4 or WebM with in-tree writers
  (a few boxes and elements; no dependency, `CGO_ENABLED=0` kept). It cuts a fragment
  at a keyframe after 10 s or 4 MiB (hard cut at 1.5 times either) and stores it at
  once; at most three wait for the object store. A keyframe that changes the picture
  size or the SPS or PPS starts a new part.
- **Fragments live under `<stem>.parts/`, the finalized file is `<stem>.<ext>`.** The
  spec put both under one name (`x` finalized, `x/init` and `x/<seq>` fragments);
  MinIO does not let an object and a prefix share a name. The file name is the
  spec's.
- **The finalizer streams.** It writes the header (`sidx` or Cues, from the manifest:
  fragment sizes, durations, keyframe flags, offsets) and then copies each fragment
  through a pipe to a put of known size, so a one-hour video is never in memory. A
  fragment that is missing or the wrong size ends the file at the one before.
- **Crash recovery is the row, not replication.** hello-sip writes a stream's row
  when it opens and after every stored fragment; a node that dies leaves it in
  `recording`, and hello-control's sweeper ends the rows nobody wrote for ten
  minutes (`failure = interrupted`) and finalizes what they list. A takeover carries
  audio only (the video relay is the node's own), so the spec's replicated video
  recording state and "part 2 after a takeover" are not built: the part the dead node
  held is kept, the taker records audio. This matches how the door video and the
  browser legs already behave on takeover.
- **Retention, quota and the notice.** `recording_settings.video_retention_days`
  (default 30; the table is new, audio has no retention job) and
  `HELLO_RECORDING_VIDEO_QUOTA_GB` (default 200, 0 for none). The quota is read by
  hello-sip (no new video recording above it) and hello-control (a warning at 80
  percent and a gauge). The notice plays `recording-notice-video` when the
  recording includes video and the operator uploaded it, else the plain
  `recording-notice`.
- **Web clients are told what is recorded by an INFO** of type
  `application/x-hello-recording` (`recording: on|off`, `video: yes|no`), sent on the
  dialog of each browser leg when the recording starts, gains video or stops. The
  softphone shows a REC badge, with a camera mark when video is in it. Desk phones
  hear the notice. The spec's "both sides that are Hello clients" is this.
- **The player slaves the video to the audio** (`RecordingPlayer`): the WAV is the
  clock, each `<video>` muted and set to the audio's time minus the stream's offset
  on play and seek and by a two-second check against a 100 ms drift, parts one after
  another with a "recording gap" marker, a download when the browser cannot play the
  codec. The manifest never names an object.
- **Conference-room video (11) forwards the active speaker; it does not compose
  and it is not an SFU.** The reasons are in docs/conference.md (composing needs
  decoding and encoding, an SFU is a second media engine beside the one that anchors
  and records). The room already measures each participant's audio every 20 ms, so
  the speaker is the loudest participant with video above a gate and 1.5 times the
  speaker's level, after a 1.5 s dwell; each viewer has its own outbound stream
  (SSRC, payload type, offsets that continue across a switch) and waits for a
  keyframe of a new source, asked for with a PLI every 250 ms and given up on after
  two seconds. The speaker sees the one before. All the decisions about who is shown
  whom, the wait and the queueing happen under the room's lock together, so a late
  packet of the old speaker cannot land among the new one's.
- **One codec per room**, fixed by the first participant with video (H.264
  Constrained Baseline, else VP8); a later one joins the video only with that codec.
  Phones and door stations are H.264 only.
- **A per-room Video switch** (`conference_rooms.video`, migration 00099, default
  on): bandwidth is the operator's to spend per room. Off answers every video line
  with port 0.
- **A forwarded participant carries video too**: the leg to the node that mixes the
  room is the one URI destination that takes video, so a room that lives on another
  node still shows video; each end of that leg takes a video slot.
- **Not built for rooms:** recording of room video (the room's recording is its audio
  mix, stored in segments as before) and re-homing video on a takeover.
- **Tests use headless pion and plain SIP/RTP clients** (a DTLS-SRTP browser stand-in,
  UDP video phones, a door station phone); real phones, door stations and browsers
  are on the hardware list.

## Kamailio HA flake, second cause: the 100 Trying of a draining instance (2026-11-13)

- Symptom (main push 42882475277, kw run): in "graceful loss of the active
  instance" the takeover re-INVITE got a 100 from the VIP, never a final
  response (40 s), and the phone never saw it. #292 and #296/#298 fixed other
  halves (the Lease, the masquerade, 2xx retransmission); this one is the INVITE
  itself.
- Root cause, from the victim's own Kamailio log (kamailio-ha job, run
  43378502793, iterations 11 and 12; the old logs went with the deleted pod, so
  the job now records them): kube-vip drops the VIP address at SIGTERM while
  Kamailio runs on for the 5 s preStop, and every Kamailio socket is bound to
  the VIP. A request that arrived in the ~100 ms before the address went got
  tm's automatic 100 Trying; then `sendto` towards the phone failed with
  "Network is unreachable (101)", t_relay failed, and the 477 error reply to
  Hello failed the same way. A UA that has had a 100 stops retransmitting its
  INVITE (the test's stub does, a real one does), so nothing ever reached the
  survivor. Reproduced in a container (VIP address removed between the 100 and
  the relay): 100, then silence.
- Product gap, not a test artefact: the same happens to any INVITE (a phone's
  too) in that window of a rolling restart or node drain. Keeping the instance
  forwarding through the preStop is not possible: the VIP address is kube-vip's
  to remove, and it does so at SIGTERM.
- Fix: `modparam("tm", "auto_inv_100", 0)`; route[RELAY] sends the 100 with
  `t_reply` only after `t_relay()` succeeded. An INVITE that could not be sent
  on gets no answer, the sender retransmits, and the instance that holds the VIP
  next serves it. (A 100 for a request whose 2xx was already relayed is
  suppressed by tm.) Needs the Kamailio config in the chart's files/, the kw
  ConfigMap copy and its checksum annotation: deploy/kuvryn-sync/kw changes.
  Regression guard: TestKamailioInvite100AfterRelay; the kamailio-ha job
  (KINDHA_GRACEFUL_REPEAT=n repeats the graceful loss) shows it end to end.

## Integrations P10: SCIM 2.0 server (2026-11-14, feat/scim)

- **One engine for push and pull.** A SCIM connector is a directory connector
  of kind `scim` whose driver reads the resources identity providers pushed.
  Every SCIM write runs the directory planner on that one user (absence rule
  off: a push says nothing about other users) and applies the plan at once
  (`Engine.Push`); a manual preview or apply on a SCIM connector re-maps every
  stored resource, which is how a changed mapping or rule reaches users pushed
  earlier. A SCIM connector has no schedule and no credential.
- **Resources are kept as sent** (`scim_resources`, data in canonical
  attribute case, never a password; members in `scim_group_members`), so a GET
  answers what the IdP set. Hello assigns the SCIM `id` (a UUID); it is also
  `users.external_id`, the stable link. The IdP's `externalId` is stored and
  filterable but is not the link (Okta does not always send one).
- **A push that would be a conflict is refused** (409 uniqueness or 400
  invalidValue/mutability by the planner's reason), recorded on the SCIM run,
  and nothing is stored, so the IdP shows the error and retries after a fix.
  A user created with `active: false` is stored without a Hello user until it
  is active.
- **Adopting local users** needs the connector switch `adoptExisting` (default
  off): a push has no preview to tick, so the switch is the administrator's
  confirmation (I-6). Local admins are never adopted.
- **DELETE** marks the resource deleted (404 to SCIM) and disables the Hello
  user; the same userName or externalId pushed again revives the resource with
  its id, so it re-links and re-enables the same Hello user.
- **Guard (I-25):** disables counted per rolling hour from the SCIM runs'
  `disable` rows against 20 percent of the linked users, at least 5. Over it:
  429, nothing applied, the connector paused (`paused_at`), and a `scim` run
  with status `needs_confirmation` holding the halted rows. Confirming that run
  (existing confirm route, or Resume SCIM in the console) clears the pause and
  restarts the window (`guard_since`); the refused requests are not replayed,
  the IdP sends them again.
- **Changes are recorded on one SCIM run per connector and hour**, so the Runs
  view shows what the IdP did with the existing change table.
- **Tokens:** a `scim_tokens` table (SHA-256 only, `hello_scim_` prefix), at
  most two active per connector so a token can be rotated without a gap (the
  spec says "one bearer token"; two is a superset for rotation). Issue needs
  `secrets` (route `POST .../scim-token` as specced), list/status/revoke are
  admin; issue and revoke are excluded from MCP (credentials), list and status
  are MCP tools.
- **Group rules** match a pushed group by its Hello id or its externalId
  (Entra's object id), so a rule can be written before the group is pushed.
  Membership changes re-plan only the affected members, and only when a rule
  names the group.
- **Groups also accept PUT** (spec lists GET/PATCH/DELETE): Okta's group push
  may PUT; additive. `/Bulk`, `/Me` and `/.search` answer 501.
- **ETags** are supported (meta.version, If-Match 412, If-None-Match 304),
  though not required by the spec, as they cost one version column.
- **Rate limit** 600/min per token is kept in memory per replica, like the
  recording-link limiter.
- **Writes of one connector are serialised** with the engine's advisory lock
  key `hello:dirsync:<id>` (blocking, 15 s, then 503), so a SCIM write never
  interleaves with a manual apply.
- **Default extension mapping** is only Hello's pbx extension attribute: the
  spec's `phoneNumbers[type eq "work"]` fallback would turn every full phone
  number into an `extension_invalid` refusal; it is documented as an opt-in
  (`phoneNumbers[work]` with the digit transforms).
- `/scim/v2` is mounted only with `HELLO_PUBLIC_URL` (meta.location needs it);
  without it `/scim/` answers a SCIM 404 and the token routes 503.
- **Sign-in after P11.** Merged with P9/P11/P12: an enabled SCIM connector
  needs `options.loginMethod` `oidc` with `oidcProvider` `entra` or `google`
  (ldap_bind stays LDAP-only); the SCIM form asks for it. No default is
  guessed from the IdP, since Okta or JumpCloud may push users who sign in with
  Microsoft or Google. A push applies range auto-provisioning like a sync run
  when the connector has it on.
- Not tested: real Entra ID, Okta and JumpCloud tenants (lab list in
  docs/scim.md and the tracker).

## Takeover re-INVITE restates the session timer (2026-11-15)

- Context: #348 follow-up. `haReinvite` sent no `Session-Expires`, so a phone
  could read the takeover re-INVITE (a session refresh in RFC 4028 terms) as
  turning the dialog's timer off until the survivor's next refresh.
- Decision: a homed leg is bound to its dialog's slot in the call's session
  timers (`haLeg.bindSession`: caller/callee by `haNewCall`, the party's side
  for a parked call, the callee for an IVR dial-out, the caller for a queue
  callback). Every re-INVITE `haReinvite` sends on a leg whose dialog has a
  timer carries `Supported: timer`, `Session-Expires: <interval>;refresher=`
  `uac` (Hello refreshes) or `uas` (the phone does), and `Min-SE`; a leg
  without one carries none. The call's live timer is the single source of
  truth, so the mid-call homed re-INVITEs (hold mirroring, park retrieve on a
  taken-over call) restate it too.
- 422: the interval is raised to the answer's `Min-SE` and the re-INVITE is
  resent with the next CSeq, up to `sessionMaxRetries`, inside the same
  `haReinviteTimeout` budget; the survivor then refreshes at the raised
  interval.
- A 2xx with `Session-Expires` sets the granted interval (never below the
  current one or `Min-SE`) and the refresher it names (RFC 4028 7.2). A 2xx
  without one leaves the timer as it was, like the refresh path: Hello covers
  phones without RFC 4028 support anyway.
- Test globals (#347/#348 follow-up): `bossAssistantLead`,
  `checkSyncTimeout`, `pushPollInterval`, `mwiPollInterval` and
  `presencePollInterval` are atomic tunables; `ivrLaunch` became a Server
  method with a per-server `ivrLaunchHook`, since its one test builds its own
  Server.

## Messaging wiring between items 2 to 6 (2026-11-14, feat/messaging-wiring)

What the merged messaging items left between them, and the choices made where
the spec left them open (each chosen by default; change one by editing this
entry and the code it names).

- **Router to desk phone was already wired.** Item 5's `Store.afterAssign`
  calls item 3's `deliverLastToDeskPhone` for both a direct user or extension
  route and a text queue's assignment, in the assigning transaction. Nothing
  changed; `TestRouterDeliversToDeskPhone` now proves it (direct route, queue
  strategy, and no delivery for a `ring_all` shared pool).
- **Automatic texts on every channel.** `systemText` (the first-contact text
  and the closed-hours reply) had a SIP trunk branch only. A chat site now
  stores it sent and puts it on the visitor's stream (shown from "Support"); a
  Twilio or Telnyx number queues it for the send worker (opt-out checked again
  at claim). A carrier with refused credentials, a peer that is not E.164, an
  unregistered adapter or a trunk channel without a trunk write a line naming
  the text and the reason. An automatic text is not an agent's answer: it does
  not reset a visitor's 200-message cap. The inbox labels it "Automatic reply".
- **Chat availability follows the route.** A site is live when whoever its
  address routes to is there: a text queue while a member is staffed (the call
  queues' login and pause; capacity and the idle-voice flag are ignored, so a
  chat waits in the queue rather than going offline), a user or extension's
  user or a ring group's users while one has the inbox open, the supervisors'
  pool (also the fallback for a target that is gone) while an operator or
  administrator has. Outside the hours the site is closed unless the
  after-hours action routes elsewhere, in which case that target decides.
  Valkey unreadable counts static members only, as the router does. The site
  cap still applies. Chat sites gain a read-only `availability` on list and get.
- **One schedule.** `messaging.Hours` was a copy of `messaging.Schedule` (same
  fields and JSON), and a chat site stored its own hours next to its address's
  routing schedule, so the widget and the router could disagree. `Hours` is
  removed; the site's `hours` field reads and writes its address's `schedule`.
  Migration `00100_messaging_one_schedule` copies a site's non-empty hours to
  its address (a site's hours win over a schedule set through routing) and
  drops `chat_sites.hours`; down restores it from the schedule. The API keeps
  `hours` on chat sites (same JSON); OpenAPI's `ChatHours` is folded into
  `MessagingSchedule`. The chat sites page edits hours and the route in one
  "Routing and hours" dialog (the routing dialog), not in the site dialog.
- **M-18 is not on main.** The media pipeline (`message_media`, MinIO under
  `msg/`, sniffing and allowlist, `GET /api/v1/inbox/media/{id}`) belongs to no
  delivered phase and no PR has built it, so chat attachments and the site logo
  stay unbuilt (the route is 404, nothing half-wired).
- **MCP:** no new operation; chat sites and routing stay excluded, so the
  doctable is unchanged.

## Integrations P12: range auto-provisioning for directory users (2026-11-14)

Built from spec `integrations.md` I-11 on the P8 directory engine. The spec
left the points below open; each was **chosen by default (the conservative
option)**, not by the user.

- **Who gets a number.** A user the connector creates, adopts or manages whose
  mapped extension is empty, or names no extension, when provisioning is on and
  the user is in the optional source-group filter (`autoProvision.groups`,
  empty = everyone). A value that is invalid, duplicated or linked elsewhere
  stays the conflict it was. A user that already has its synced extension keeps
  it even when the directory value names a missing one. In `create_only` mode a
  user is provisioned once (Hello owns the extension afterwards, so a deleted
  one is not recreated); `link_only` users are provisioned like
  `authoritative` ones.
- **A full range is a conflict, not a user without a number.**
  `range_exhausted` leaves that user unchanged (not created, not linked), like
  every other conflict, so nobody appears half set up; the administrator widens
  the range. Not overridable.
- **Stable numbers need state.** Migration 00101 keeps the allocated number on
  the user (`users.provisioned_number`) and marks the extension
  (`extensions.provisioned_by`, set null when the connector is deleted). A
  re-run prefers that number even outside a changed range, relinks its
  extension when it still exists unlinked, and recreates it with the same
  number when it was deleted (at the next full run: an incremental run reads
  only changed users).
- **Preview predicts, apply allocates.** The preview shows the number the
  planner predicts from the current dial space; the apply allocates in the
  user's own transaction under the `hellocfg` lock through the configuration
  store (the same dial-space rules and whole-configuration check as a console
  create). A number taken in between is replaced by the next free one and the
  run row shows the number actually given; a full range at that point is an
  `error` row with reason `range_exhausted`. Enabling or changing provisioning
  changes the sync fingerprint, so the next scheduled run is a full one.
- **"Free" and "reserved prefix".** Free = no extension, ring group, IVR menu,
  conference room, queue or voicemail group number, no park slot, and no
  feature or paging code that is a prefix of it or it of one (feature and paging
  codes start with `*`, so today that never matches a digit number; kept as a
  guard). Hello has no other reserved-prefix setting; outbound route patterns
  are not numbers an extension can take, and the whole-configuration check runs
  anyway.
- **What is created.** The extension (named from the display name, else the
  username, trimmed to 100 printable characters), its voicemail box (on, mailed
  to the user's email; with `voicemail: false` the box exists switched off, as
  for every extension) and, unless `device: false`, one desk device named
  after the number (`2000`, else `2000-2`, ...) whose secret is generated and
  dropped. Ranges hold 2 to 10 digit numbers of equal width (zero-padded ones
  allowed) and at most 100,000 numbers. Overlapping ranges of two connectors are
  allowed: the lock keeps them apart.
- **Following the user.** While provisioning is on, a display-name or email
  change renames the provisioned extension and updates its voicemail email, but
  only while each still holds the value provisioning gave it (an administrator's
  own edit wins).
- **Deprovisioning is unchanged (I-7).** A disabled user keeps the extension,
  the voicemail box and its messages; its devices are switched off with the
  existing `disableDevices` option and come back when the user does. Deleting a
  connector keeps the extensions and clears their mark.
- **Audit and metrics.** Each provisioning writes `provision user`, `create
extension` and `create device` rows with the actor `directory:<connector>`;
  `hello_directory_provisioned_total{connector_kind}` counts them.
- **New read route** `GET /api/v1/directory/connectors/{id}/range` (admin,
  exposed over MCP as `getDirectoryRange` like the other directory reads):
  size, numbers used, next free and the users holding a number.

## Clients item 3: Diagnostics Clients tab, WebRTC status and the S-52 series (2026-11-14)

Builds on the web devices of #282 and the WebRTC legs of #324, #341 and #343.
Choices:

- **The live legs reach hello-control through Valkey, per node.** A call with a
  browser leg is not replicated (its ICE and DTLS state is the node's own), so
  no existing record holds it. Each hello-sip node with WebRTC media publishes
  its legs (at most 100), its last 20 ICE and DTLS failures and its counters
  every 10 s under `hello:webrtc:node:<id>` with a 30 s TTL, and removes the
  key when it stops. `GET /api/v1/webrtc/status` reads every node's.
- **The status answers 200 with the parts it could read.** It combines
  PostgreSQL, Valkey and the TURN servers; one that cannot be read is named in
  `unavailable` and its part is empty, instead of a 503 for the whole answer.
- **TURN reachability is a STUN Binding request from hello-control** (UDP or
  TCP; coturn answers it without credentials), and a TCP connection for a
  `turns:` URI, whose certificate may come from a private CA hello-control
  does not hold. The result is reused for 30 s. A leg counts as relayed
  through TURN when ICE selected an address of a TURN server.
- **Push is shown as not configured** with a sentence: the push gateway is
  "not now" (decision 2026-10-25), so its series and alerts are documented as
  coming with it, not stubbed.
- **Labels.** `hello_srtp_auth_failures_total{direction}` uses the side of the
  call the failing packet came from (`from_caller`, `from_callee`), since an
  authentication failure only exists on receive. It counts every encrypted
  leg (SDES phones too), so it is registered with the media series, not only
  when WebRTC is on. `hello_webrtc_ice_state_total{state}` counts `connected`
  (first authenticated check), `restarted` (new remote ufrag) and `failed` (no
  check before the DTLS handshake gave up, or by the end of a call at least
  5 s after its DTLS session started; a browser fork that lost is not a
  failure). `hello_webrtc_candidate_type_total{type}` counts the client's own
  (local) candidate type of each end-of-call report; the status endpoint shows
  both sides from the stored reports. `hello_client_auth_failures_total{reason}`
  has `expired`, `username`, `credentials`, `unknown_device` (a client name
  whose device is gone: revoked, disabled or deleted) and `throttled`.
- **`hello_webrtc_dtls_handshake_seconds` is a series of its own** next to the
  existing `hello_dtls_handshake_seconds` (all DTLS-SRTP legs, phones too), so
  the browser handshakes can be watched apart.
- **`hello_ice_credentials_issued_total` is renamed
  `hello_turn_credentials_issued_total`** (the spec's name), and now counts
  only answers that carry a TURN credential, from `/me/ice-servers` and from an
  enrolment (which also returns ICE servers). It was not on a dashboard yet.
- **`hello_client_registrations{kind}` is per receiving node** (like
  `hello_sip_registrations`), so the cluster value is the sum. To back the
  spec's alert "zero registered WSS clients while some are enrolled",
  hello-control adds `hello_client_devices{kind}` (enabled web and mobile
  devices, read at scrape time).
- **MCP:** `getWebRTCStatus` is a read tool (viewer) and
  `hello://webrtc/status` a resource; the troubleshoot-call prompt points to it
  for browser calls. The answer carries no credential or secret.
- **`TestClientMetrics`** moves the registration, auth-failure, video-slot and
  WebRTC leg series with real calls through hello-sip; the push part of S-52's
  test comes with the push gateway.

## Integrations P9 and P11: Google Workspace and Entra connectors, single sign-on (2026-11-14)

Builder decisions within spec I-20 to I-22 and I-26 to I-30 (the spec left
these open or to the implementation):

- **Entra delta state lives in the connector's sync mark**, not on the run
  row: the mark already carries the incremental state of LDAP, and a run that
  fails keeps the previous link.
- **A full run ends with `$deltatoken=latest`** to take the delta link without
  paging the whole delta, since the full list was just read.
- **Fallbacks to a full read**: `410 Gone` (`delta_expired`), any `@removed`
  entry (`delta_removed`: absence needs a complete list, the engine's absence
  rule depends on it), more than 1,000 changes (`delta_large`), and a changed
  membership hash of the groups the rules use (`groups_changed`). Changed
  users are re-read 15 ids per `$filter=id in (...)` for a full `$select`.
- **Guests are skipped by default** (an `includeGuests` switch turns them on).
- **Group rules read `transitiveMembers`** rather than `/groups/{id}/members`
  (spec I-22), so nested groups count as they do with the AD in-chain rule.
- **Domain-wide delegation is its own `authMode` (`delegation`)** instead of
  `service_account` plus a `subject` (spec I-21): a `subject` on the default
  mode is refused, so delegation is never switched on by filling one field.
- **Identity keys**: Entra's `oid` (with `tid` pinned to the tenant), Google's
  `sub`; both equal the ids the connectors link by, so a synced user is found
  without email.
- **Email matching only for Google synced non-admins**: exactly one synced,
  non-admin, non-break-glass user with a verified email of a listed domain.
  Entra never matches by email (`oid` is always there). Local users and
  administrators link themselves from their profile while signed in.
- **JIT is off by default**; a JIT user gets the `user` role and password `!`.
- **SSO keeps Hello's own TOTP**: a user with 2FA continues on the login page
  with the challenge carried in the URL fragment (`/login#mfa=`), never a
  query string or log.
- **The OIDC binding cookie is `SameSite=Lax`** (the provider's redirect is a
  top-level cross-site GET), path `/api/v1/`, 10 minutes; the session cookie
  stays `Strict`. `state` and `nonce` are single use in Valkey (`GETDEL`).
- **Login method is a connector option** (`loginMethod`: `oidc` or
  `ldap_bind`, `oidcProvider`), with the earlier `ldapBindLogin` config flag
  read as `ldap_bind`. A connector whose users have no way in cannot be
  enabled (`no_login_method`), nor an `oidc` one before its provider is.
- **One `credential` body field** for the new kinds' secrets, accepted next to
  `bindPassword` (one of the two per request); the `auth_method` CHECK is left
  as it is (`local` for everyone).
- **The Google SSO provider requires domains** (`hd` check); a personal Google
  account is never accepted.
- **Google scopes**: delegation and service-account tokens ask only for the
  scopes the run needs (users, plus groups and members when rules use them);
  consent asks for all three once. A grant with any scope outside the
  allow-list is refused. Replacing an `oauth_admin` secret drops the refresh
  token, so consent is granted again.
- **Adoption by a connector now clears the local password** (`!`), matching
  created users, and every set-password route answers `409
managed_by_directory`.
- **Break-glass guards are in the store**, under the same admin row lock as
  `last_admin`, so two concurrent changes cannot remove the last one. Demoting
  or disabling a break-glass admin unflags it, subject to that guard.
- **LDAP bind login throttle**: 5 failures per user and per address in 10
  minutes (Valkey), checked before any bind, so Hello locks before AD's own
  lockout does.
- **With P12 auto-provisioning** (merged after this was built): Google and
  Entra connectors provision like LDAP ones; an adopted user loses its local
  password in the same transaction that may allocate its number; a
  break-glass admin is skipped before any provisioning; releasing a
  connector's users keeps their numbers and extensions (unmarked) and gives
  no password back. Migration renumbered to `00102`.
- **All new operations are excluded from MCP** (`x-hello-mcp`): sign-in,
  secrets and identity linking are browser or admin-console flows.

## Messaging item 7: AI triage, drafts and auto-reply (2026-11-15, feat/messaging-ai)

Builder choices within the approved spec (M-30, M-30a; user decision 5 of
2026-10-25). None changes what the user approved.

- **Migration 00103 (`messaging_ai`, placeholder 00210 until merge).** Additive only. The one
  CHECK change (route kind `ai_triage`) goes through
  `pg_temp.hello_check_values`. The spec said the `ai_*` columns of
  `msg_addresses` came with P1; they did not, so this migration adds them, with
  `ai_message_runs`, `msg_faq`, the conversation's AI fields and
  `conversation_messages.ai_run_id`.
- **Where runs are queued.** In the ingest transaction (trunk texts and
  chat), only when the number's mode is not `off` and no kill switch is set,
  and never for STOP, START or HELP. A run that fails records a code and moves
  on; the text is in the inbox before any model call.
- **Follow-up runs.** A finished triage queues the draft (mode `draft`) or the
  auto-reply (mode `auto`). An auto-reply that does not send queues a draft,
  unless the customer opted out, it is spam, a keyword, stale or the window is
  closed. A failed triage still lets the draft run, and turns an auto-reply
  into a handoff.
- **Kill switches at three levels plus the environment.**
  `messaging_settings.ai_enabled` (all AI), `ai_auto_enabled` (auto-replies),
  `msg_channels.ai_enabled` (per channel; a chat site's channel or the number's
  mode act per site), the number's mode, and `HELLO_MSG_AI_AUTO=false`
  (deployment-wide, auto-replies only; helm `control.messagingAIAuto`). Every
  switch is read again when a run starts and again in the transaction that
  sends.
- **Confidence thresholds and allowed intents.** The spec asked for an
  intent list and "confidence". Triage and auto-reply both report a confidence
  (0 to 1), and each number has a least confidence (default 0.8) and the
  intents the AI may answer (empty = every intent but `other`). The inbox-wide
  intent list defaults to question, sales, support, billing, appointment,
  complaint. "Never on sensitive topics": an admin list of topics (default
  medical, legal, emergency, self-harm, harassment, payment dispute, personal
  data) that the triage flags as `sensitive`, which always hands off.
- **Holding line.** Sent on every handoff (urgent, negative, sensitive, a
  handoff word, the cap, low confidence, no FAQ, an answer the guardrails
  refuse, an AI failure), once until a person replies, only when the number has
  one (default empty). It is stored as a system message tied to the run.
- **Handoff flag.** `conversations.ai_handoff_at` and its reason; the inbox
  shows "Needs a person" until a person replies.
- **Triage queue suggestions.** The model is given the names of the enabled
  text queues (configuration, not customer data) so its `suggestedQueue` can be
  checked. It is applied only on a number whose route is the new `ai_triage`
  kind: the router waits up to 30 seconds for the first text's triage, then
  places the conversation in the suggested queue or the route's own queue. On
  other routes the suggestion is shown, not applied.
- **Revert.** `POST .../conversations/{id}/ai/revert` (and `/me/...`) removes
  the tags the AI added (still present), lowers a priority it raised (while
  still high) and clears its spam flag; the triage stays, marked reverted.
- **Grounding check.** An auto-reply must cite at least one of the number's
  enabled snippets by id. Every link in it must be found, exactly, in a
  snippet. A reply that holds a redaction placeholder is refused. The tools are
  `lookup_faq` (word overlap over the snippets) and `handoff`; both are
  read-only.
- **Audit.** Turning auto-reply on or off for a number
  (`ai_autoreply_enabled` / `_disabled`), each channel switch
  (`ai_enabled` / `ai_disabled`), the settings (`update_ai`), FAQ create,
  update and delete, and each AI-sent message (`ai_autoreply` by `system:ai`,
  the message id only).
- **MCP.** One new tool, `getInboxAIStats` (counts only, operator +
  `messages`), taught in `hello-call-handling`. The settings, the FAQ and the
  revert are excluded like the rest of messaging.
- **Not verifiable without a real model.** Triage and draft quality, an
  injection test set and redaction against real number formats are on the lab
  list in docs/inbox.md. Every test runs against test/fakellm or go-ai-sdk's
  mock model.

## Messaging M-18: the media pipeline (2026-11-15, feat/m18-media)

- **Bucket and keys.** Attachments live in the existing media bucket
  (`hello-recordings`, as the faxes) under `msg/<channel>/<yyyy>/<message>/<sha256>`
  for inbound media, as the spec says. An upload has no message yet, so its key
  is `msg/<channel>/<yyyy>/upload-<media id>/<sha256>`; a chat site's logo is
  `msg/site/<site>/logo`. A new `internal/messaging/media` package holds the
  sniffing, the allowlists, the MinIO store (with an in-memory fake), serving and
  the workers; the store keeps MinIO out as before.
- **Rows outlive their message until the file is gone.** `message_media` points
  at its message and conversation with `ON DELETE SET NULL`; retention and an
  erase delete conversations as before, and the sweeper (on the inbox
  maintenance lease) removes the file, checks it is gone with a stat, and only
  then drops the row. The spec says "MinIO objects first, rows after"; this
  order keeps that invariant (no object without a row) without the store
  touching MinIO. An erase removes the files at once in the API, verified, and
  reports `mediaObjects` and `mediaObjectsPending` (left to the sweeper when
  MinIO cannot be reached).
- **Media retention.** New setting `retentionMediaDays` (default 90, 1 to 3650,
  spec M-19). A file past it is removed and its attachment marked `expired`
  (reason `retention`); the message stays. An upload never sent goes after 24 h.
- **Sniffing and the allowlist.** `http.DetectContentType` plus PDF, AMR, 3GPP
  and vCard; the declared type must name the same class. Besides SVG and HTML,
  a file is refused as `active_content` when it contains markup or script
  markers anywhere (`<script`, `<html`, `<svg`, `<!doctype`, `<iframe`, ...,
  `javascript:`) or, for an image or a PDF, a ZIP end record in its tail (a
  GIFAR). A plain-text file holding markup is refused on purpose.
- **Outbound attachments: website chat only.** Per the user's LAN-only decision
  (no public listener), Twilio and Telnyx cannot fetch an MMS's media, so a
  carrier or SIP trunk conversation refuses `mediaIds` and uploads with `409
media_unsupported`, and the composer offers **Attach** only in a chat. An
  agent's file to a chat follows the chat's rules (images and PDF, 5 MiB), the
  same as a visitor's.
- **The public link is built but not mounted.** `media_links`, mint and redeem
  (256-bit token, SHA-256 kept, 15 minutes, 5 uses) and `media.LinkHandler` for
  `GET /m/{token}` exist and are tested, ready for the public listener (N1);
  nothing serves them on the LAN.
- **Inbound MMS.** No carrier delivers inbound texts in this build (webhooks
  and reconcile are not on), so nothing calls `AddInboundMedia` yet outside
  tests; the worker, Twilio's and Telnyx's `FetchMedia` and their host checks
  are complete and tested against fakes. Twilio: the account's own
  `api.twilio.com/2010-04-01/Accounts/<sid>/` URLs, redirects to
  `*.twiliocdn.com` or the CDN bucket path on `s3-external-1.amazonaws.com`;
  Telnyx: `*.telnyx.com`, the API key sent only to `api.telnyx.com`. Both host
  lists are unverified and on the lab list. Credentials are dropped on any
  redirect to another host.
- **Chat attachments.** A visitor's file is its own message
  (`POST /chat/v1/attachments`, multipart `file`, `clientMsgId`, optional
  `text`), counted against the 5 per hour limit and the message limits. The
  frame fetches a file with the token in the header and shows it from a blob
  URL (the frame's policy adds `img-src blob:`, the iframe `allow-downloads`).
  The UI nginx gets an exact `location = /chat/v1/attachments` with a 6 MB
  body limit; every other chat request stays at 32 KB.
- **Desk phones.** A phone cannot show a file: a text with attachments ends
  with `[N attachments: open the inbox to see them]`, and a file alone arrives
  as that line.
- **DSAR export (messaging item 8).** `Store.ConversationMedia(ctx, convID)`
  lists every attachment of a conversation with its object key, type, name,
  size and SHA-256, and `media.Objects.Open` reads the file; the export can
  stream them from these.
- **No virus scanning** (not specced; the spec says none is bundled). The chat
  site's attachments switch warns about it.
- **MCP:** the new routes are excluded (customer files are private data; the
  logo is a binary download and chat site settings stay excluded), so the
  doctable is unchanged.

## Messaging item 8: events, reports, export, redaction (2026-11-15, feat/messaging-8-reporting)

- **Events carry a hash, delivery adds the content.** The inbox's `EventSink`
  writes `conversation.created`, `conversation.assigned`,
  `conversation.resolved`, `message.received`, `message.sent`,
  `message.status` and `message.optout` into the integrations pipeline
  (`integration_events`, the stream, webhooks, CRM and Slack hooks). A stored
  event has ids, channel, direction, state, assignee, queue and `peerKey`, a
  keyed MAC of the number (`HELLO_SECRET_KEY`, purpose
  `messaging-event-peer`, 16 bytes hex), never a number or a text. An
  endpoint's `includePeer` and `includeMessageBody` (migration 00105, admin,
  audited per change, off by default) are applied at delivery by reading the
  inbox, so an erase or a redaction between the event and the delivery is
  honoured and the history and stream never hold content. The body is only
  added to `message.received` and `message.sent`. A failed read retries the
  delivery as an internal failure (`enrich_failed`, no strike against the
  endpoint).
- **Inbox events are not for every event reader.** The stream, the history
  and tickets hide `conversation.*` and `message.*` from credentials without
  the `messages` scope or below the operator role, the inbox's own access
  rule. An endpoint with an extension list gets none (they are about no
  extension).
- **Emit points fixed while wiring.** `conversation.created` is emitted before
  the first message event of a new conversation (ingest, chat, first contact);
  claims, routing placements and assignments, a reply that claims, and returns
  to the queue (stale or orphaned) emit `conversation.assigned` (no assignee
  for a return); `FinishPoll` emits the real final status.
- **Reports.** Totals and groups by channel, queue, agent (assignee) or day
  (system zone or `timeZone`), at most 366 days. First response counts only
  conversations a customer started (`origin` inbound), measured to the first
  agent reply (`first_response_at`); automatic texts do not count. Resolution is start to `resolved_at`.
  Spam is excluded. Durations are count, average, p50, p90. An open
  conversation already over a target counts as a breach. CSV export, csv-safe
  cells. `getInboxReport` is exposed to MCP (counts only); the CSV is not.
- **SLA targets on their own endpoint.** `GET/PUT /api/v1/messaging/sla`
  (`messaging_settings.sla_first_response_seconds` default 900,
  `sla_resolution_seconds` default 86400, 0 off), not in
  `/messaging/settings`, so item 7's settings changes do not collide.
- **DSAR export.** `GET /api/v1/inbox/export?peer=` (admin), a zip of
  README, `conversations.json` (with opt-outs) and one JSON per message.
  Notes are excluded unless `notes=true`. At most 50,000 messages (422
  `too_large`). Audited with counts (`media=` too).
- **The export carries M-18 attachments, files included.** The export
  bundles the message bodies, so it bundles the attachments' files too: each
  message JSON lists its `media` (id, direction, status, type, size, file
  name, SHA-256, `file`), and each file Hello holds is copied from MinIO into
  `media/<conversation>/<id>.<ext>`, named by id and sniffed type, never by
  the sender's file name (no path tricks in the archive). Attachments of
  notes follow `notes`. A chat visitor's uploads not sent yet are part of
  their data (an erase removes them), so `uploads.json` and
  `media/uploads/` hold them. Without a media store and with files to export
  the route answers 503 instead of a silently incomplete archive; a file the
  sweeper removed meanwhile (`ErrNoObject`) is listed with an empty `file`;
  any other MinIO error breaks the stream (headers are out) and is logged.
- **Redaction.** `POST .../messages/{messageId}/redact` (admin) blanks the
  body, sets `redacted_at`, deletes the SIP text-queue copies, queues the
  carrier deletion, emits `conversation.updated` and audits. System lines are
  refused (409 `not_redactable`); a second redaction is a no-op.
- **Redaction removes the message's attachments the way an erase does.** In
  the redaction's transaction the attachments are detached (`message_id`,
  `conversation_id` NULL, `attached_at` set, `source_ref` cleared so nothing
  is fetched again) and their public `media_links` deleted; after commit the
  API removes each file, checks it is gone (`media.RemoveVerified`) and drops
  the row, the erase's helper. A file MinIO cannot remove now stays an orphan
  for the media sweeper. The report adds `mediaObjects` and
  `mediaObjectsPending`; the message comes back with `media: []`. An
  attachment is removed rather than kept with a "redacted" mark: its row has
  no customer content once the file is gone, and the message already says it
  was redacted. The erase now deletes the `media_links` of erased files in
  its transaction too, so a link stops working before the file is gone.
- **Erase also deletes the SIP text-queue copies** (`messages` rows linked to
  the conversation's messages or to the number), which it left behind.
- **Carrier deletion is a durable queue.** `msg_provider_redactions` (unique
  per channel and carrier id); the request works its batch for 20 s and
  reports counts; `messaging.Redactor.Run` retries on every control replica
  (row leases), back-off one minute doubling to six hours, 12 tries (about a
  day); refusals fail at once; finished rows are kept 30 days.
  `hello_msg_provider_redactions_total{provider,result}`.
- **Twilio deletes, Telnyx cannot.** Twilio: `DELETE Messages/{sid}`; 404 is
  done; a refusal to delete a message still in flight falls back to blanking
  the body (`POST Body=""`). Telnyx's API has no per-message delete
  (`DELETE /messages/{id}` only cancels a scheduled send), so its rows are
  `unsupported` and the report reads the messaging profile's
  `redaction_enabled` to say whether Telnyx masks its own records. Both wait
  for a real-account check.
- **Supervisor-view report.** `GET /api/v1/inbox/audit/supervisor-views`
  (admin) reads the existing `supervisor_view` audit rows: per-person totals
  and the rows newest first, paged by id. Excluded from MCP: staff supervision
  is reviewed by an administrator in the console.
- **UI.** Inbox reports page (operators; targets form for admins), Messaging →
  Privacy page (export, carrier deletions, supervisor views), a Redact action
  in the conversation view (admins), webhook inbox options, inbox events on
  the integration events page. Zapier gets seven inbox triggers with the two
  options as Zap inputs; n8n and Make get the events and both options.
- **Migration 00105** (after main's 00104 media): the webhook options, the SLA
  targets, `redacted_at`, the redaction queue and three report indexes.

## Items 26 and 27: Replaces of confirmed dialogs, authenticated INVITEs to phones (2026-11-14)

- Who may replace a confirmed dialog (S-17 leaves it open): only a phone of the
  same extension as the party whose dialog is named, so a user moves their own
  call between their own phones (call pull, a shared line) and nobody else's.
  The pickup scope (S-50) still governs early dialogs only: a colleague may
  answer a ringing call but never take over an answered one. A trunk's side, a
  click-to-call's own leg and a browser or video call are never taken over.
  RFC 3891 matching is strict for confirmed dialogs (Call-ID and both tags,
  either order, 481 otherwise; early-only gives 486); early dialogs keep the
  Call-ID match pickup always had.
- How the call continues: the caller's dialog of a plain call is taken over
  by making the new phone's call the call (it gets the callee leg and the
  relay, like an attended transfer's bridged call), so it stays an ordinary
  call and replicates for in-call HA. The callee's dialog cannot be carried
  that way (both remaining dialogs would be UAS dialogs of Hello), so the two
  calls are linked like a picked-up call; a linked pair is not replicated
  (park_ha.go), and the original's record is deleted at the switch so no
  survivor re-INVITEs a phone that left. A linked call's own party can be
  taken over again (the call moves back). Across nodes the holding node does
  the rewiring through a new park-mailbox command, `replace`, and the pair is
  linked across nodes; a linked call whose peer is on another node is refused
  (486), because the peer names it by the replaced dialog's Call-ID.
- The new phone must offer the call's codec (488 otherwise): the other party
  is not re-INVITEd, so it hears no change. A party the old phone had held is
  re-INVITEd sendrecv.
- livestate.Call gains `legCallIds` (the ringing legs, then the answering leg),
  so a Replaces landing on another node finds the call; a ringing call is
  republished at its first ring. The BLF dialog-info names the watched
  extension's dialog (call-id, local-tag, remote-tag, direction) when the call
  is on the same node; without it no phone could build a Replaces header from
  a BLF key. List (eventlist) parts stay compact, without them.
- Attended transfer before answer: the transferor's ringing consultation call is
  now cancelled (487 to its INVITE, CANCEL to the target) before the blind
  transfer rings the target for the transferee, so the transferor has no call
  left up and the target does not ring twice. The abort that does it gives 487
  and a cancelled CDR, not the drain abort's 503.
- Authenticated INVITEs (S-22): every INVITE Hello sends a phone (calls, pages,
  intercom, door chimes, page tests, click-to-call) answers a 401 or 407 with the
  device's credentials and is re-sent as a new transaction of the same attempt
  (same Call-ID, From tag and To, CSeq + 1, new Via branch); at most two
  answers, for a stale nonce. In-dialog re-INVITEs and UPDATEs (hold, re-anchor,
  transfers, session refreshes, takeover re-INVITEs) are answered the same way
  in their dialog. BYE is not: P2346 authenticates INVITEs only.
- The realm: the stored HA1 answers a challenge in the SIP domain's realm; a
  phone's own realm (a WP uses its server "host:port", as found for check-sync)
  needs the sealed device password. hello-sip reads it through a new
  `Deps.DeviceSecrets` (the store's DeviceSecret, as check-sync), once per
  device and realm, and keeps only the derived HA1 in memory, keyed with the
  device's stored HA1 so a new secret misses the cache. This is a PostgreSQL
  read on the first challenged call of a device, off the SIP transaction path
  (the leg's goroutine).
- P2346 is now written 1 on every Grandstream WP account, and the per-account
  P2347/P2346 of further lines (+100 per account) with it; the 2026-11-09 entry
  that left P2346 out is superseded. Not yet confirmed on a phone (hardware
  check: a WP836, WP822 and WP856 with P2346 = 1 ring, hold and transfer). The
  Poly, Snom and Fanvil TR-069/SSH keys stay a hardware item: no vendor
  document on hand confirms them.

## One INVITE transaction per dialog; park commands per call in order (2026-11-15)

CI's `-race` run caught `TestCallParkHold/across_nodes` racing inside sipgo:
two hold commands for one parked call ran in two goroutines, and both
re-INVITEs read the dialog's stored INVITE, whose Content-Type sipgo parses
lazily and caches (a write on a shared request). Beyond the memory race this
was SIP glare (RFC 3261 14.1), a possible duplicate CSeq (sipgo loads,
increments and stores the dialog CSeq non-atomically), and a hold then resume
could reach the phone reversed, leaving it held.

- **Park commands for one call run in order.** The park loop queues each
  call's commands (by Call-ID) and one goroutine drains them. A hang-up does
  not queue (a BYE may cross a pending re-INVITE), and pickup and door-open,
  which name no call, run as before.
- **Hello starts one INVITE transaction per dialog at a time.** `reinvite`
  and `reinviteA` wait on a gate keyed by the sipgo dialog session; a homed
  leg (incall-ha) has its own mutex, taken by `haReinvite` and the homed
  session refresh. A session-timer re-INVITE takes the same gate, and its
  timeout starts once it may go. A phone's re-INVITE relayed onto the other
  dialog (direct and anchored-without-SDP paths) is Hello's INVITE there too
  and takes that dialog's gate. Hello queues rather than retrying on 491:
  the conflict is its own, so waiting is cheaper than a 2 to 4 second backoff.
- **The ACK goes out before the gate opens** where the 2xx is ACKed without
  a body (`reinviteAcked`: park retrieve and linked hold). The transfer flows
  keep their own ACK placement for now; the 3PCC ones ACK with the other
  party's answer, after a second re-INVITE.
- **No lazy reads of the shared INVITE.** The re-INVITE and its late ACK set
  `Content-Type: application/sdp` themselves (every body Hello sends there is
  SDP) instead of copying the dialog INVITE's header.
- **A linked party's re-INVITEs are handled one at a time.** Its next
  re-INVITE can arrive as soon as the previous 200 is out, while the previous
  handler is still telling the peer. `linkedReinvite` holds a per-call lock
  from first look to `holdPeer`, so a hold then resume is sent to the peer's
  node in that order, once each (CI caught the reversal through the new test).
- **A homed leg's CSeq is spent when the request is built,** under the leg's
  lock, so a BYE or NOTIFY built during a re-INVITE never shares its CSeq, and
  the re-INVITE's 200 only moves the counter forward. `doOn` (#357) still
  moves it past a re-sent challenged request's CSeq; the challenge re-send
  stays inside the gate, as one INVITE attempt.

## TLS hop: outbound TLS requests leave from an ephemeral port (2026-11-15, fix/tls-path-test-isolation)

- **Product bug, not test isolation.** Hello pins the requests its client
  originates to the SIP UDP listening address (`WithClientConnectionAddr`,
  `fromListener`, for DSCP and NAT). sipgo applied that address to TLS
  requests too: it bound the TCP dial to the UDP socket's IP:port and looked
  the connection up by that local address. The first TLS connection to one
  edge then carried every later TLS request whatever the Path named, a second
  connection could not bind while the first (or its TIME_WAIT) held the port,
  and the dial failed with `bind: address already in use` whenever an
  unrelated TCP socket held that port number (the flaky
  TestSIPCallsPhoneThroughTLSPath).
- **Fix.** The server's client has a `hopRequester` TxRequester (also used by
  the pickup "elsewhere" requester) that clears `Laddr` on requests over a
  reliable transport. UDP requests stay pinned to the marked SIP socket; TLS
  requests dial from an ephemeral port and sipgo reuses the connection by
  destination. Via is unchanged (the advertised address). DSCP is unchanged:
  the mark was only ever on the UDP socket, never on a TLS connection.
- **WriteRequest is for ACKs only.** With a TxRequester set, sipgo's
  `Client.WriteRequest` goes through it; `hopRequester` writes an ACK
  straight to the transport and sends anything else as a transaction. Hello
  only writes ACKs that way; TestDSCPOutboundOnMarkedSocket, which wrote a
  bare OPTIONS, now sends it as a client transaction from the listener.
- **Regression test.** TestSIPCallsPhonesBehindTwoTLSEdges: phones of one
  extension registered through two edge TLS sockets must each get an INVITE.

## Provisioning parity items 15 and 29 finished: WP side key and speed dials, backups from the HA node list, Snom/Fanvil backups, Yealink/Poly SRV (2026-11-14)

Spec `.procoder/specs/phone-feature-parity.md` S-12 and S-20, following #259,
#262 and #312.

### Item 15 (S-12): side key and speed dials as first-class fields

- **Side key.** `phone_groups.side_key` (ptt, alarm, none; default ptt) and
  `phones.side_key` (NULL inherits), migration `00106_side_key.sql`.
  API `PUT /api/v1/phone-groups/{id}/side-key` (MCP `setPhoneGroupSideKey`) and
  `sideKey` on `PATCH /api/v1/phones/{id}`; the phone view carries
  `sideKeyChoices`. Console: phone group editor and phone form.
- PTT and paging are one firmware value ("PTT/Paging", 0), so Hello offers one
  choice for both rather than a "paging" value that would write the same 0.
- Keys per model, from Grandstream's templates (config-template-10212026.zip):
  WP8x6 `P82317` 0/2/4 (PTT/paging, alarm, none); WP856 `P82317` 0/4 (its
  "PTT Button" has no alarm value); WP820 multi-function button `P22258` 0/4
  (its 2, SAFE/Panic call, needs the WP820 panic-call keys, which Hello does not
  write, so alarm is not offered); WP810/822/825 none. The key is now written
  always (before: `P82317 = 0` only with PTT or a paging key), so a changed
  choice reverts. A choice the model lacks, or alarm without the group's alarm
  destination, renders PTT/paging (the factory default); the API refuses a
  phone-level choice its model lacks. A raw override of the same P-value still
  wins (overrides are applied after the template).
- **Speed dials.** A WP's BLF entries are no longer refused: they are written as
  speed dials (spec: "Hello BLF entries on a WP phone are offered as speed
  dials"). Slots: WP8x6 number keys 2 to 9 and 0 (long press; key 0 mode/
  account/label/value `P8682` to `P8685`, key n `P8690 + 4 (n-2)` on; mode 0
  Speed Dial, -1 None); WP810/822/825 and WP820 speed dial keys 2 to 9 and 0
  (`P23011 + 5 (n-2)` account, `P23013 + 5 (n-2)` number; key 0 `P23001`,
  `P23003`); WP856 multi-purpose keys 1 to 30 (`P23000 + 5 (n-1)` mode 0, then
  account, description, value). Correction to the 2026-11-09 note "The WP8x6
  template has no speed dial key": it has the number keys above (section
  "Number Keys", commented out with mode -1 default). Every slot is written,
  empty when unused, so a removed entry is cleared; the WP856 is capped at 30 so
  that clearing stays bounded (the template has 160 MPKs). The WP856 MPK also
  has a BLF mode (1): the spec asks for speed dials on every WP, so mode 0 is
  written; switching the WP856 to real BLF lamps is a one-value change if the
  user wants it. The API refuses more entries than the model's slots
  (`speedDialSlots` on the phone view).
- Unconfirmed on hardware (hardware list): every side key and speed dial key
  above on a WP8x6, WP822, WP820 and WP856; in particular the WP820 speed dial
  keys (commented out in its template) and whether the WP8x6 number keys are
  the long-press digits the "Number Keys" section suggests.

### Item 29 (S-20): backups from the HA node list, Snom and Fanvil, Yealink and Poly SRV

- **HA node list.** hello-control's cluster poller (every heartbeat) keeps the
  SIP addresses (`HELLO_SIP_ADVERTISED_ADDR`) of the READY hello-sip members,
  by node ID (`api.SIPNodeList`). When `HELLO_PROV_SIP_SERVER` is one of them
  (phones register with a node directly; it is also the default of that
  setting) the other nodes, at most two, are the phones' backups. Behind an
  edge (Kamailio, a VIP, a load balancer; kw and Helm) the member addresses are
  in-cluster names phones cannot reach, so there are none from the list: the
  edges are not in the membership and hello-control has no runtime list of
  them. `HELLO_PROV_SIP_BACKUP_SERVERS` stays as an override (the spec allows
  it) and is how the second Kamailio of a VIP-less pair is named. The source is
  shown on the Provisioning settings page (`sipBackupServers`,
  `sipBackupSource`, `sipSrv` on `GET /api/v1/prov/settings`).
- **Snom**: `user_outbound` per identity lists the primary, then every backup,
  `host:port;transport=udp|tcp`, comma separated (Snom Service Hub,
  user_outbound: "Starting version 8.7.x multiple comma separated values are
  supported for e.g. failover scenarios"; registrar_whitelist stays empty, its
  default, which user_outbound must otherwise match). Written empty without a
  backup, so removing them reverts. This replaces the earlier "no Snom key".
- **Fanvil**: `SIPn Backup Addr`, `Backup Port`, `Backup Transport` (0 UDP, 1
  TCP), `Backup TTL`. Key names from a Fanvil X5 configuration export as copied
  in Nethesis' Tancredi template `fanvil-X5.tmpl` (third party; no Fanvil
  document with the export names could be fetched). Backup Addr is written
  empty without a backup. Unconfirmed: on the hardware list.
- **Yealink and Poly SRV**: with `HELLO_PROV_SIP_SRV`, a Yealink or Poly on UDP
  registers with the name, port 0 and the DNS-NAPTR transport (Yealink
  `transport_type = 3`; Poly `transport = "DNSnaptr"`, which the UC Software
  guide defines as "address a host name and port 0 or Null: NAPTR then SRV
  look-ups, RFC 3263"). The earlier objection ("DNS-NAPTR is not plain SRV")
  does not hold: with no NAPTR records, RFC 3263 clients fall back to SRV, and
  Hello's documented record set is SRV only. TCP and TLS phones of those vendors
  keep the address and backups (DNS-NAPTR replaces the transport choice).
  Yealink's port 0 and its UDP-only SRV fallback (`naptr_build` default 0,
  not written) are from the administrator guide: hardware list.
- Also fixed in passing: further lines (2 and up) now get the backup on
  Yealink, Poly, Snom and Fanvil, and a backup follows the primary's transport
  (it was UDP only); a Yealink or Poly further line on a TCP phone now
  registers over TCP like line 1 (it was written UDP).

## TestAlwaysAnchorPolicy flake: a late ACK turned a caller hangup into "no ACK" (2026-11-15)

- Symptom (main-derived run 43679381386, integration job): the CDR of an
  anchored call had empty CallerCodec/CalleeCodec although media mode and
  trace were right.
- Root cause: the caller's BYE was processed before its ACK. sipgo's ReadBye
  sets the dialog state Ended, which wakes the answer's WriteResponse with "No
  ACK received"; the ACK handled right after runs ReadAck, which sets the state
  back to Confirmed without checking. answer() then saw a live dialog, took the
  "no ACK from caller" path and won the end race: a ResultFailed CDR, whose
  answer time (and so codecs and billable time) is cleared, on a SIDE_SYSTEM.
- Product bug, not a test artefact: any phone whose ACK and BYE are reordered
  or handled concurrently (a fast hangup) got a failed CDR for an answered call.
- Fix: callerHungUpUnacked checks the dialog's context as well as its state;
  the context is cancelled when the dialog ends and never comes back. Used by
  the b2bua answer, the IVR/queue/directory solo answer, page, fax and pickup
  (pickup had no BYE-overtook-ACK check at all). Regression guard:
  TestCallByeBeforeLateAck, which lands the ACK through a test-only
  unackedHook (fails on the old state-only check).
- Follow-up flagged by the PR's integration run (43687055816):
  TestVideoRecordingPolicy/quota_used_up saw the quota refusal counted twice.
  startVideoRecording is asked by both the recording's start and the video's
  start (videoStarted); depending on timing both ran the policy checks, so one
  call's refusal was counted and traced once or twice. A metric over-count,
  fixed in the product: refuseVideoRec settles the refusal once per call
  (call.vrecRefused) and later asks return false silently. Regression guard:
  TestVideoRecordingRefusedOnce (eight concurrent refusals, counted once).

## Messaging privacy follow-ups: AI output on redaction, media race (2026-11-15, fix/msg-redaction-followups)

- **What a redaction clears of the AI.** An AI run reads the conversation's
  texts up to its own message, so the result of every run of the
  conversation from the redacted message on is cleared (`result = NULL`),
  plus the run that wrote the message when the AI wrote it
  (`conversation_messages.ai_run_id`). Conservative on purpose: a later run
  whose context window no longer held the message loses its result too.
  The conversation's `ai_triage` keeps its classification (intent, urgency,
  sentiment, tags, queue) and loses only its `summary`, when its triage read
  the message or a later one: the summary is model-written text from the
  customer's words, the labels are not, and routing still reads the queue.
  Tags already applied to the conversation stay (agents edit them). The AI
  message itself (an auto-reply) is not redacted with the customer's text:
  it is what was sent, and an administrator redacts it separately.
  Redacting a note touches no AI output (the AI never reads notes).
- **Runs in flight.** In the redaction's transaction, a running run of the
  conversation from the message on is marked `stale` with error `redacted`
  (FinishAIRun then stores and sends nothing); queued ones are row-locked
  (the claim skips locked rows), so they run after the commit on the
  blanked text. FinishAIRun also marks a run stale when a message it may
  have read (`id <= message_id`) has `redacted_at` later than one minute
  before the run was created, which covers a run queued while the
  redaction was committing (`redacted_at` is the redaction's start time).
  The redaction locks its message `FOR NO KEY UPDATE` (not `FOR UPDATE`) so
  a FinishAIRun inserting a run for that message does not wait on it while
  holding the run and conversation rows the redaction wants next.
- **Erase.** Already complete for the AI: runs cascade with the
  conversations, the triage lives on the conversation row, and a run in
  flight finds its row gone and stores nothing. Logs, audit rows, events
  and webhook payloads hold no message text (webhook bodies are read at
  delivery); the full-text search index is an expression index on the body
  and follows it. No other AI-derived copy exists (no FAQ suggestions; the
  FAQ is written by administrators; MCP has no inbox text tools).
- **Export.** `conversations.json` gets each conversation's `ai` (triage
  with summary, applied changes, spam, handoff) and each message file its
  `aiRuns` (kind, state, result, times); the audit row counts `ai_runs=`.
- **Media race, no migration.** The tombstone is the existing state: an
  erased attachment's row is gone, a redacted one has `message_id NULL`.
  FinishMediaFetch only records a file on a row still on its message;
  otherwise it answers `media.ErrGone` and the worker removes (and checks)
  the file it stored, unless another row of the same message names the same
  key (a worker whose lease lapsed finishing after the one that recorded it).
  An upload whose FinishMediaUpload finds no row (inbox API and chat) removes
  its file the same way.
- **Orphan sweep.** `Worker.SweepOrphans` lists `msg/` (new
  `Objects.List`) hourly on the maintenance lease holder and removes files
  older than `media.OrphanAge` (1 hour, far past the 5-minute download lease
  and an upload request) that no row names. Keys are looked up through the
  row they belong to (`media.ParseKey`: an upload by attachment id, a
  download by message id, plus the rows off their message), so the lookup
  uses indexes; a key of another shape (a chat site's logo) is never
  removed. Counted in `hello_msg_purged_total{kind="media"}`.

## TestPrackAndReplaces: the target answers by caller, not by arrival (2026-11-15)

CI flaked `attended transfer before answer, answered after the cancel`
("timed out waiting for BYE to the transferor's first call"). It was the
test, not the product. The target's callee chose between ringing and
answering by the order its handlers ran, but the phone reports an INVITE
before it runs the callee. So the test could see the consultation INVITE,
send the REFER and get the transferred call's INVITE while the
consultation's handler had not yet run. The transferred call then counted
as first and rang forever. Every earlier check still passed, because the
consultation was cancelled either way. A forced one-second delay on the
first handler reproduces it every time. The callee now rings the INVITE
From the transferor's extension (the consultation) and answers the others.
Hello itself BYEs the transferor as soon as the transferred call is
answered, whatever the order.

## MoH takeover flake: the test read the dead owner's re-INVITE (2026-11-15, fix/moh-takeover)

- **Test bug, not a product bug, and older than #357/#359/#364/#366.**
  TestMusicOnHoldTakeover fails at abdf747c too (1/15). Later merges fail
  0-2 times in 10 runs each, with no step change at any of them. Logs from a failing run show the
  taker re-homing the call in `hold` with `moh:1:b`, starting the music and
  writing it to the held phone's address from its new relay port.
- **Cause.** The owner relays the holder's hold to the callee as a re-INVITE
  (CSeq 2). It stays in `b.reinvites`, so "takeover re-INVITE to the held
  callee" returned that one, and the test counted only packets from the dead
  owner's relay port. It passed only when the taker's relay happened to bind
  the same freed port. The holder also never ACKed its hold 200 (`dcs.Do`
  alone), so no relayed ACK reached the callee, and the "ACK to the held
  callee" wait was satisfied only by the taker's ACK.
- **Fix.** The holder ACKs its hold, as a phone does. The test drains the
  relayed hold re-INVITE and its ACK before the takeover, and checks that the
  takeover re-INVITE's CSeq is past the hold's. No product change.
- **Siblings: leaked call media exhausted the RTP range.** No other takeover
  test reads a stale re-INVITE. The `-count=30` sweep of
  `TestMusicOnHoldTakeover|Takeover` still failed, for another reason: a test
  PBX's teardown cancels Serve but never closes the media of calls still up
  (a killed owner's, a call the test never hung up). Every test PBX shares the
  default range 20000-21000, and some takeover tests (TestConferenceTakeover,
  TestTakeoverKeepsEventState among them) left 2 ports bound per run for the
  rest of the process. Around iteration 10 TestConferenceTakeover got
  `bind conference RTP socket: ... :20499: address already in use` (503), and
  after that most takeover tests timed out. The leaked music streams also kept
  sending RTP to ports other tests reused. startPBX's cleanup now runs
  releaseCallMedia once Serve has returned. It closes every remaining call's
  relay and conference party; killMedia uses it too. Measured with a probe
  that counts bound ports in the range after each test: 0 for every takeover
  test with the change; 2 more per run for those tests without it. This is test-only: in
  production the process exits with the node.
- **TestTrunkLeaseTakeover left as is.** It failed 1 in 30 sweep runs and 2
  in 120 runs alone, in three different ways. The failing run with logs
  coincides with a macOS maintenance sleep (pmset: asleep 17:25:16-17:25:59,
  a 42 s wall-clock gap inside a run the test timed at 1.28 s). Its 300 ms
  leases cannot survive the machine sleeping, which is not a product or
  test bug. Local loops now run under `caffeinate -i`.

## momo Phase 1 pilot: the pin, internal/db and internal/crm (2026-11-16, feat/momo-p1-crm)

- **Pin and scope.** `github.com/azrtydxb/momo v0.14.0`, exact, as the query
  layer only. Goose stays the migration tool; no migration, no schema
  change. `momo.yaml` has no `migrate` section. Hello imports only momo's
  root, `driver/pgxdriver` and `momolog` in production code; the model gate
  test adds `gen/config`, `gen/fromdb` and `migrate` (test binary only).
  None of momo's MySQL or SQLite packages is built, and `go.mod` requires
  none of them; `go.sum` carries their checksums because momo is one module,
  which is Go's module-graph bookkeeping, not a dependency. The momo command
  is run as `go run github.com/azrtydxb/momo/cmd/momo@v0.14.0`, which builds
  it in its own module.
- **One handle per pool, no new pool.** `internal/db.New(*sql.DB, log)`
  wraps the existing pool with `pgxdriver.FromSQLDB`; hello-control and
  hello-sip (which also builds `crm.PG` for caller-name lookups) each pass
  their own `*sql.DB`. Logging: momo's slog hook (`momolog`) on the
  telemetry logger with `component=db`, debug per statement, warn above
  `db.SlowQuery` (500 ms). Argument values are never logged
  (`WithArgLogging` off), because sealed tokens and secrets are arguments.
- **Hand-written SQL with @name binds, not builders.** Every crm statement
  keeps its SQL text; `$n` became `@name` and `momo.Named`. The upserts with
  `ON CONFLICT ... WHERE`, the `FOR UPDATE OF l SKIP LOCKED` claim, the
  `UNION ALL` stats and the joins to users/extensions stay raw, as the plan
  recommends. A reused bind (`@dnd`, `@status`, `@now`) renders one bind per
  use, so each use carries an explicit cast where PostgreSQL must infer the
  same type (`@dnd::boolean` added in SetCalendarSettings, `@now::timestamptz`
  in MarkRenewals; Finish and ListLogs already cast).
- **Models are generated but not yet used by crm queries.** The task asked
  for the models and the gate; crm reads with small per-query scan structs
  (`Query[T]` by column name, aliases for computed columns such as
  `has_secret`) so each SELECT reads exactly the columns it read before
  (a model read would also pull `client_secret_enc` and the sealed tokens
  into memory where only `IS NOT NULL` was read). Model inserts and
  builders come per package where column churn hurts (plan 3.1); PR B
  shows the named-bind route for the widest insert.
- **Errors are unchanged for callers.** Zero rows go through a generic
  `one()` that returns `sql.ErrNoRows` (G7: momo still has no single-row raw
  helper), so the existing `notFound` mapping and `UserForExtension`'s
  "no user" result are untouched. `unique()` accepts
  `momo.ErrUniqueViolation` or the `*pgconn.PgError` 23505 check it always
  made (both hold through momo's error). Other errors now read
  `momo: <kind> (op=...): <driver error>`; `errors.Is`/`errors.As` still
  reach the driver error and `context.Canceled`.
- **Transactions.** CreateConnector and UpdateConnector use `momo.DB.Tx`
  (commit on nil, rollback on error, the fn's error returned as is, so
  ErrNotFound and the unique check behave as before). The hook-side methods
  take the event ingest `*sql.Tx` and run on `db.AdoptTx`, which never
  commits it. Difference noted, accepted: with `FromSQLDB` a transaction
  outlives the context passed to Begin; every statement still gets the
  request context.
- **Contract test.** `TestRepoContractMem` and `TestRepoContractPG` run the
  same assertions (error kinds, secrets, tokens, single-use state, the call
  log state machine, mappings). It found one divergence: Mem accepted
  `SaveUserConnection` for user 0, which PG refuses with ErrNotFound. Mem
  now refuses it too (test double aligned to production; no caller passes
  user 0, the services check first). Other known differences are listed
  in the test's comment and left alone: Mem overwrites granted scopes with
  an empty list on a refresh, returns ErrNotFound from Finish and
  SetCalendarState for an unknown id, and orders ClaimDue by id.
- **Statement build test without a database.** `TestPGStatementsBuild`
  calls every PG method on `momo.NewOffline(dialect.Postgres)`; a statement
  that does not build is `ErrValidation`, one that built is the offline
  handle's `ErrUnsupported`. The pull request tier (no database) therefore
  catches a misspelled `@name`.
- **Model gate.** `TestMomoModelsMatchSchema` (test/integration) migrates a
  scratch database with goose, inspects the `introspect.only` tables with
  `Config.OnlyTables`, runs `fromdb.Generate` with the `momo.yaml` settings,
  and fails when a committed model body differs, a file is missing or
  orphaned, the generator errors, or it warns about anything but the known
  dropped constraints (the foreign keys to `users`, which has no model). A
  second check compares the schema the generated code declares
  (`MomoMeta().TableSchema`) with the inspected one, so a model regenerated
  without `momo generate` fails too. The failure prints the regenerate
  commands and the expected files; the commands are in docs/database.md.

## momo Phase 1: the CDR insert on named binds (2026-11-16, feat/momo-p1-cdr)

- **Named binds, not a model insert.** The CDR statement is one
  data-modifying CTE: the `cdrs` row, then its page, follow-me and queue
  legs from JSON arrays through `jsonb_array_elements ... WITH ORDINALITY`,
  which momo cannot build (G10). A model insert would split it into four
  statements and a transaction, which changes the writer's failure modes,
  so the SQL text stays and only its 40 `$n` became `@name` binds named
  after their columns (`insertArgs`). `TestInsertBindsEveryColumn` pins
  that each `cdrs` column is bound by its own name in the same position;
  the old tests that read `args[32]` now read `answered_by`.
- **Frozen SQL.** The rendered statement and the id lookup are registered
  in the package's own `momo.QueryRegistry` (`cdr.Queries`) and frozen by
  `momotest.QueryGolden` in `internal/cdr/testdata/queries/postgres.sql`
  (rewrite with `MOMO_UPDATE_GOLDEN=1`). A package registry instead of
  `momo.RegisterQuery`'s process-wide one, so the golden file lists only
  this package's statements whatever else the test binary links; the
  mechanism is the same. The golden file shows the statement PostgreSQL
  receives: identical to the old text except that the leg arrays are
  `$38`/`$39`/`$40` (page, follow-me, queue) and video is `$37`.
- **Same pool, same execution mode.** hello-sip's writer keeps its pgx pool
  (4 connections). `internal/db.NewPool` wraps it with momo's native pgx
  driver and passes the pool's own `DefaultQueryExecMode` (pgxpool's
  default caches prepared statements), so the statement reaches PostgreSQL
  as before instead of in momo's default exec mode.
- **Writer behaviour unchanged.** Queue size, `Enqueue` dropping when full,
  the retry loop with its backoff, the shutdown flush and the 5 s write
  timeout are untouched; only `write` and `publish` changed. The
  call.completed id lookup now always runs through the momo handle (before,
  only when the handle could `QueryRow`, which the pool always could); zero
  rows still emit the events with cdrId 0 and log at debug.
- **Stacked on the crm pilot.** This branch needs `internal/db` and the
  momo pin from feat/momo-p1-crm and merges after it.

## Two flakes: Grandstream DTLS/ICE test token, CANCEL crossing a 200 (2026-11-16, fix/flakes-cancel-gs)

- **TestGrandstreamDTLSAndICE: the random token spelled a forbidden word.**
  The check that Yealink, Poly and Fanvil write no DTLS or ICE key searched
  the whole lower-cased config for "dtls", or for "ice" and "nat". Every one
  of those configs already contains "ice" ("device", "voice"), so the check
  was really "dtls" or "nat". renderSample puts a fresh NewToken (52 random
  base32 letters) in the provisioning URL that each config carries. A token
  containing "nat" (about 1 render in 650, 3 renders per run) or "dtls"
  failed the test. Forcing a token that starts with "nat" or "dtls" failed it
  for all three vendors.
- **Fix (test only).** renderOtherVendor renders the vendor config and
  removes the sample's token before the word check. It fails if the token
  cannot be found, so the check cannot pass vacuously. The word check is
  unchanged. The test now also checks that the DTLS and ICE settings change
  no byte of these vendors' configs, which catches a key under any name. The
  same token could spell "srtp" in TestGrandstreamSRTPMode's matching
  check (about 1 render in 21,000), so that check uses the helper too.
- **TestCallCancelRacing200: a 200 arriving after the 487 was never ACKed
  (product bug).** When a fork's CANCEL completes, sipgo's WaitAnswer takes
  the first final response and terminates the INVITE transaction. If the
  callee's 200 came first, drive ACKed and BYEd it. If the 487 came first,
  the 200 no longer matched a transaction, and Hello's unhandled-response
  handler only logged it at debug. The callee was left in a confirmed
  dialog nobody ACKed or hung up, which breaks RFC 3261 §13.2.2.4. A forking
  proxy or SBC in front of a phone sends exactly this order (it forwards a
  2xx after its 487, §16.7), and UDP datagrams can swap. The test's callee
  sent the 200, slept 100 ms, then let the transaction send the 487; the
  helper's comment already said a 487 handled first "would drop the 200". On
  a loaded CI runner (run 43822034934 attempt 1, the api package took 1283 s
  alongside it) the 200's goroutine fell more than 100 ms behind, and the
  test timed out waiting for the ACK.
- **Fix (product).** A fork whose INVITE ends without a 2xx is kept for
  64*T1, the time a UAS keeps retransmitting its 2xx (late2xx.go). The
  unhandled-response handler matches a 2xx to INVITE by Call-ID, Hello's
  From tag and the INVITE CSeq, then ACKs and BYEs it through the fork's own
  dialog (l.bye), the same path a 200 that wins the race takes.
  Remaining gap, in sipgo: a 2xx handled in the microseconds between the
  487 and WaitAnswer terminating the transaction falls into the Completed
  state, which ignores 2xx.
- **Test.** TestCallCancelRacing200 runs both orders: "200 before 487" (as
  before) and "487 before 200" (the callee sends a 487, waits 100 ms, then
  the 200). It now also checks that the ACK is the ACK of the 2xx (a new
  Via branch, the INVITE's CSeq) and the BYE is on the fork's Call-ID.
- **Rates** (`go test -race -count=50 -run TestCallCancelRacing200`, M-series
  Mac). Old test, old code: 0/50 idle, 0/1,800 under 36 concurrent copies
  at GOMAXPROCS=2. The CI failure did not reproduce locally. New test on
  old code: "487 before 200" failed 50/50 with the CI message ("timed out waiting for ACK to the callee's crossing 200"); "200 before 487" failed 0/50. New test with the fix: 0/50 for
  each order.

## momo Phase 2: internal/slack on momo (2026-11-16, momo/slack)

- **Scope of this group.** Of the packages assigned (slack, chat,
  livestate, cluster, telemetry, oauth), only `internal/slack` uses
  database/sql. The grep matches in chat, livestate, cluster and oauth are
  Valkey `Script.Exec` calls and `url.Query()`; telemetry's is
  `url.Query()`. The SQL behind `oauth.Store` and chat's webchat store is in
  `internal/store` (Phase 3). So this group is one PR, and the lease,
  heartbeat and lock timing of livestate and cluster is untouched.
- **Hand-written SQL with @name binds.** `slack.PG` holds a `*momo.DB`,
  built inline in hello-control with `hellodb.New(db, log)`. Every
  statement keeps its SQL; `$n` became `@name`. Rows are read with
  `momo.RawRows` into the same Scan destinations as before (`scanDest`,
  `scanPost` unchanged), and single-row reads go through a local
  `queryRow`, which reports `sql.ErrNoRows` on an empty result as
  `QueryRow` did (G7), so `mapErr` and Retry's not-found/conflict split are
  unchanged. `FOR UPDATE OF sp SKIP LOCKED` in Claim is as it was.
- **Casts for reused binds.** A reused `@name` is one bind per use, so
  every use carries a cast where PostgreSQL must give the uses one type:
  Update's `@reset::boolean` (twice) and `@secret_enc::bytea`, Finish's
  `@now::timestamptz` (the `updated_at` use gained the cast the
  `delivered_at` use had), and ListPosts' `@dest::bigint`, `@status::text`,
  `@before::bigint`. Before, ListPosts' `$1 = 0` made the destination and
  cursor parameters int4; they are int8 now, which matches the bigint
  columns and changes no result for any id that fitted.
- **Transactions.** `PG.tx` keeps its shape on `momo.DB.Begin`: commit on
  nil, else roll back (error ignored, as before) and return fn's error
  through `mapErr`. The rollback runs on `context.WithoutCancel(ctx)` so it
  still reaches the database when ctx ended the work; with `FromSQLDB` a
  transaction is not rolled back by its Begin context ending (noted in
  Phase 1). Enqueue joins the event ingest `*sql.Tx` through
  `db.AdoptTx`, which never commits it; the `Store` interface still takes
  `*sql.Tx`. A nil tx now runs on the pool where it panicked before; no
  caller passes nil.
- **Errors.** `isUnique` accepts `momo.ErrUniqueViolation` or the 23505
  `*pgconn.PgError` check it always made. Other errors now read
  `momo: <kind> (op=...): <driver error>`.
- **Hot path.** None. The slack store runs in hello-control (admin API,
  event ingest hook, the post worker); hello-sip does not build it. momo's
  hook adds one debug log call per statement.
- **Tests.** `TestPGStatementsBuild` runs every method on momo's offline
  handle. `TestPGStatementsBuildInTx` runs every method on a stub
  `momo.Driver` that answers queries with zero or one row, so the
  statements inside transactions build too: all 19 distinct statements
  must reach the driver with no unrendered `@` and `$1` present. Both run
  on the pull request tier. `TestStoreContractMem` and
  `TestStoreContractPG` run the same assertions on Mem and PG (error
  kinds, the nil-secret update, queue filters and de-duplication, the
  claim lease, health counters, both disable rules, manual retry, paging,
  purge, cascade on delete); the file header lists the differences left
  out on purpose. `TestPGQueue` is unchanged apart from building the store
  with `hellodb.New`.

## momo Phase 2: internal/ai/proposal and internal/ai/detect (2026-11-16, feat/momo-p2-ai)

- **Handle.** `proposal.NewStore` and `detect.Env.DB` take the `*momo.DB`
  that hello-control builds inline with `hellodb.New(db, log)` at each
  construction site. Every statement keeps its SQL text with `@name` binds.
  AI calls are untouched (still go-ai-sdk through `internal/ai`).
- **Advisory lock.** Upsert's `pg_advisory_xact_lock(hashtext('hello:ai:proposals'))`
  is now `advisory.XactLockString(ctx, tx, "hello:ai:proposals")`: the same
  function, the same server-side `hashtext` of the same text (bound as a
  parameter instead of a literal), so old and new pods contend on one key.
- **Transactions.** Upsert, Dismiss and Apply use `momo.DB.Begin` with a
  deferred Rollback rather than `DB.Tx`, so the early `return id,
tx.Commit()` and the errors returned (ErrSuppressed, ErrNotOpen,
  ErrNotFound, ErrNotBatch) are exactly as before. Apply still begins on
  `context.WithoutCancel(ctx)` and writes its outcome and commits on it.
  Difference (as in Phase 1, accepted): with `FromSQLDB` a transaction
  outlives the context given to Begin, so a cancelled request no longer
  rolls the transaction back by itself; the next statement fails on the
  cancelled context and the deferred Rollback ends it.
- **Same results for callers.** Single-row reads go through a local `one()`
  that returns `sql.ErrNoRows`, so ErrNotFound, ErrNotOpen and the
  suppression check map as before; `List` still returns an empty, non-nil
  slice. `FOR UPDATE SKIP LOCKED` in `lock` is unchanged. The detectors'
  `QueryRow` on the last explanation sample became a read of at most one
  row (no row is the zero state, as `isNoRows` gave). `history` now wraps a
  row scan error in "read <kind> samples" too (it wrapped only the query).
- **Reused binds.** momo renders one bind per use of an `@name`, so a use
  that PostgreSQL must type on its own carries a cast: `@status::text` and
  `@source::text` in List, `@status::text` in Apply's outcome update. The
  supersede query's `@session::uuid IS NOT NULL AND session_id = @session`
  is typed by the cast and the column.
- **Statements pulled into helpers for the build test.** Upsert's three
  statements (`suppressed`, `refresh`, `insert`), Dismiss's update
  (`markDismissed`) and Apply's outcome update (`recordOutcome`) are small
  functions on `momo.Querier`, as are `lock`, `audit` and `supersede`;
  detect's last-explanation read (`lastExplained`) and the
  never-registered read (`neverRegistered`) likewise. The offline tests
  (`TestStatementsBuild` in both packages) call each on
  `momo.NewOffline(dialect.Postgres)`, so a misspelled `@name` fails the
  pull request tier. Neither package has an in-memory fake of its own, so
  there is no mem/pg contract test; the DB-backed tests (env_test,
  batch_db_test, the detector tests) cover behaviour in the integration job.
- **Detector test fixtures** run on the same momo handle (`momo.Exec`,
  `momo.Scalars`) with their native `$n` placeholders, which momo accepts
  on PostgreSQL.

## momo Phase 2: internal/webhook (2026-11-16, momo/webhook)

- **Same SQL, named binds.** Every `webhook.PG` statement keeps its text;
  `$n` became `@name` and `momo.Named`. Computed columns got aliases
  (`events_json`, `extensions_json`, `pending`, `dead`, `payload`) so the
  per-query scan structs match by name; aliases do not change what a
  statement reads. The `text[]`/`bigint[]` literals (`pgArray`, `idArray`)
  and their casts stay. `ListDeliveries` still builds its WHERE from fixed
  fragments, now with `@endpoint_id`, `@status`, `@before` and `@limit`.
  The handle is built inline in hello-control (`hellodb.New(db, log)`).
- **Locks on momo's advisory package, keys unchanged.** The claim lock is
  `advisory.XactLock(tx, 0x68656c6c6f776864)` and the create lock
  `advisory.XactLockString(tx, "hello:webhooks:create")`, which send
  `SELECT pg_advisory_xact_lock($1)` and
  `SELECT pg_advisory_xact_lock(hashtext($1))`: the same function, key and
  transaction scope as the raw statements, so old and new replicas exclude
  each other during a rolling update. `TestPGLockKeys` pins both statements
  and keys. `FOR UPDATE SKIP LOCKED` in the lease and `FOR UPDATE` in
  UpdateEndpoint are unchanged.
- **Transactions.** `PG.tx` is `momo.DB.Tx` (commit on nil, rollback on
  error) with the old `sql.ErrNoRows` to `ErrNotFound` mapping. `Enqueue`
  runs in the event ingest `*sql.Tx` through `db.AdoptTx`, which never
  commits it; the TxHook contract is unchanged.
- **Errors unchanged for callers.** A generic `one()` returns
  `sql.ErrNoRows` for zero rows (G7), so ErrNotFound and Redeliver's
  ErrInFlight check behave as before; other errors carry momo's prefix with
  the driver error and `context.Canceled` still reachable.
- **Hermetic test on a stub driver, not the offline handle.** Almost every
  method opens a transaction, which `momo.NewOffline` refuses at BEGIN, so
  `TestPGStatementsBuild` runs each method on a small recording momo driver
  that answers one row (booleans true, integers 1), driving Claim through
  its lease and Finish through disabling the endpoint, and zero rows for
  the not-found paths. A misspelled `@name` fails as `ErrValidation` on the
  pull request tier. Column names are not checked there (the stub names no
  columns); the existing mem/pg rig (`forEachStore`), which runs the same
  service and dispatcher assertions on Mem and on PostgreSQL, is the
  contract test and proves them in the integration tier.

## momo Phase 2: internal/events, and internal/queue has no SQL (2026-11-16, momo/events-queue)

- **internal/queue is not converted: it has no SQL.** The six call sites
  the inventory counted there are Valkey Lua scripts (`joinScript.Exec`
  and the like in `valkey.go`). The PostgreSQL `queue.CallbackStore` lives
  in `internal/store/queue_callbacks.go` and moves with `internal/store`
  (Phase 3).
- **The ingest transaction is momo's, the hooks still get a `*sql.Tx`.**
  `events.Service.DB` is now a `*momo.DB` (built inline in hello-control
  with `hellodb.New(db, log)`). `store` runs the batch in `DB.Tx` and hands
  every hook the `*sql.Tx` behind it (`pgxdriver.UnwrapSQLTx`), so
  `TxHook` keeps its signature and the crm, webhook and slack hooks write
  in the same transaction as before, raw or through `db.AdoptTx`; none of
  them commits it. A failing hook still rolls the whole batch back. A DB
  that is not over database/sql (no `*sql.Tx` to hand out) refuses a batch
  with hooks (`errNotSQLTx`) instead of running them outside the
  transaction. As in crm, a momo transaction over `FromSQLDB` outlives the
  context passed to Begin; every statement still gets the ingest context,
  and a batch whose acknowledgement fails after the commit is read again
  and found stored, as before.
- **Same SQL, named binds.** The insert, the user lookup, the retention
  purge and `History` keep their text with `@name` binds; History's WHERE
  is still built from fixed fragments, now with named binds (`@after_ms`,
  `@types`, `@limit`, ...). Rows are read with `momo.Query` into tuple
  rows. History now buffers its page (at most `MaxLimit`, 1000 rows)
  instead of streaming it; a row whose payload this version cannot decode
  is still skipped.
- **Tests.** `TestStatementsBuild` runs the insert, the user lookup, every
  History filter and the purge on momo's offline handle (pull request
  tier). `TestIngestHooksShareTheTransaction` (DB tier) proves that a hook
  sees its event row, that a raw hook write and an `AdoptTx` momo write
  commit with the event, and that a failing hook rolls back what earlier
  hooks wrote. events has no in-memory store fake, so there is no mem/pg
  contract test.

## momo Phase 2: the directory sync store (2026-11-16, momo/directory)

- **Scope.** `internal/directory/pg.go`, all 77 raw call sites (the
  QueryContext/QueryRowContext/ExecContext/BeginTx/PrepareContext/Conn
  calls), moves to momo v0.14.0. `directory.PG.DB` is now a `*momo.DB`,
  built inline at hello-control's construction site with
  `hellodb.New(db, log)`. `dirstore` has no SQL of its own: it passes the
  directory transaction to the configuration store (`internal/store`, Phase
  3), so the `Provisioner` interface keeps its `*sql.Tx`. No schema change.
- **Same SQL, named binds, positional scans.** Every statement keeps its
  text with `@name` binds. The rows are still scanned positionally by the
  existing code (`scanConnector`, `scanRun`, LoadState's loops) through
  `momo.RawRows`, whose Scan is `database/sql`'s, so NULL handling and
  conversions are unchanged; `queryRow` returns `sql.ErrNoRows` on no row,
  so `mapErr`, ClaimQueued's "nothing queued" and extAndRings' "no linked
  extension" behave as before (G7). A reused bind renders one bind per use,
  so RecordOutcome casts `@ok::boolean` and `@at::timestamptz`, update
  casts `@set_managed`/`@adopt` to boolean and ListChanges casts
  `@action::text`, the types PostgreSQL inferred for the single `$n`.
- **Transactions unchanged.** SaveConnector, DeleteConnector,
  ReplaceChanges, Apply and RecordPush run in `momo.DB.Tx` (commit on nil,
  rollback on error, the fn's error returned as is), so every audit row,
  the last-admin and break-glass refusals, the adopt rules and the revision
  bump commit or roll back together exactly as before. The configuration
  lock is `advisory.XactLock(tx, 0x68656c6c6f636667)`, which sends the same
  `SELECT pg_advisory_xact_lock($1)` with the same key; the revision
  NOTIFY is `pgxdriver.Notify`, the same `SELECT pg_notify($1, $2)` on the
  same channel inside the transaction. Auto-provisioning hands the
  transaction to the provisioner as its `*sql.Tx`
  (`pgxdriver.UnwrapSQLTx`), so the store's numbering and routing check
  run in it, under the lock, as before. loadTaken's read-only transaction
  is a momo `Begin(ReadOnly)` that is always rolled back, as before.
- **Prepared inserts.** ReplaceChanges and RecordPush prepared the change
  insert once per transaction; they now run the same statement per change
  in the same transaction (momo has no Prepare on raw SQL). Same rows, same
  order; pgx's statement cache still prepares it once per connection.
- **Per-connector lock (I-13).** TryLock pins one connection
  (`momo.Pinner`), takes `advisory.TryLockString`, the same
  `pg_try_advisory_lock(hashtext(key))`, and keeps the connection until the
  unlock. An unlock that fails discards the connection (`momo.Discarder`,
  which closes it so the pool drops it), as the `driver.ErrBadConn` trick
  did.
- **Errors.** A taken connector name is still the field error on `name`
  (`momo.ErrUniqueViolation` or the 23505 `*pgconn.PgError`, which still
  holds through momo's error). Other errors now read
  `momo: <kind> (op=...): <driver error>`; `errors.Is`/`errors.As` reach the
  driver error.
- **Tests.** `TestPGStatementsBuild` runs every PG method and every Apply
  operation on a scripted momo driver (no database, PR tier): momo builds
  each statement before the driver sees it, so a misspelled `@name` fails;
  the scripted driver answers the statements that built so each method runs
  past its first one, including those inside transactions (the offline
  handle stops at BEGIN). `TestStoreContractMem`/`PG` run the same
  assertions on Mem and PG (error kinds, the credential, the run queue,
  changes, the SCIM run of the hour, link and release, the lock); the
  known differences are listed in the test's comment. The SCIM PG test
  builds `directory.PG` on the momo handle too.

## momo Phase 2: the SCIM store (2026-11-16, momo/scim)

- **Scope.** `internal/scim/pg.go`, all 24 raw call sites plus the
  dedicated connection of Lock, moves to momo v0.14.0; `scim.PG.DB` is a
  `*momo.DB`, built inline in hello-control. The other "call sites" a grep
  finds in the package (`URL.Query()` in handler.go) are not SQL. Stacked on
  the directory PR (momo/directory): the SCIM PG test builds `directory.PG`
  on the momo handle, which that PR introduces.
- **Same SQL, named binds, positional scans.** As in the directory store:
  every statement keeps its text with `@name` binds, rows are scanned by
  the existing `scanToken`/`scanResource` through `momo.RawRows`, and
  `queryRow` returns `sql.ErrNoRows` so the mapping to ErrNotFound (a
  missing token, a moved resource version) is unchanged. FindDeleted's
  reused externalId bind is cast `@external_id::text` at each use, the type
  PostgreSQL inferred for the single `$4`. FindResources binds the hint as
  `@value`.
- **The mass-deprovision guard (I-25) and adoptExisting are unchanged.**
  Neither lives in this store: the guard counts the SCIM runs' disables
  through `directory.Store.PushDisables`/`RecordPush`/`SetPaused` (moved,
  same statements, in the directory PR) and decides in the directory
  engine (`push.go`); adoption is the service's `AdoptExisting` switch, the
  engine's plan and `directory.PG.Apply`. This store's part
  is the per-connector lock that serialises a push with a directory run,
  which keeps its key and semantics (below).
- **Lock.** Lock pins one connection (`momo.Pinner`) and takes
  `advisory.LockString` with a deadline of lockWait: the same
  `pg_advisory_lock(hashtext('hello:dirsync:<id>'))` the directory engine's
  TryLock takes, so a push and a run still exclude each other across
  replicas. A wait that fails is still ErrBusy when only the lock wait ran
  out, and the connection is discarded (`momo.Discarder`) as the
  `driver.ErrBadConn` path did; so is one whose unlock fails.
- **Transactions and errors unchanged.** CreateToken, RevokeToken and
  SaveResource run in `momo.DB.Tx` with their audit rows. SaveResource maps
  a unique violation to ErrConflict and a foreign key violation (an unknown
  member) to ErrNotFound, by momo's kinds or the 23505/23503
  `*pgconn.PgError` check it always made.
- **Tests.** `TestPGStatementsBuild` runs every method on a scripted momo
  driver (a copy of the directory package's) on the PR tier.
  `TestStoreContractMem`/`PG` run the same token, resource, membership,
  count and lock assertions on Mem and PG; the known differences are in the
  test's comment.

## momo Phase 2: the SSO store (2026-11-16, momo/sso)

- **Scope.** `internal/sso/pg.go`, all 21 raw call sites, moves to momo
  v0.14.0; `sso.PG.DB` is a `*momo.DB`, built inline in hello-control. The
  other matches a grep finds in the package (`URL.Query()` in ssotest) are
  not SQL. Independent of the directory and SCIM pull requests (no shared
  type), so it branches from main.
- **Same SQL, named binds, positional scans.** Every statement keeps its
  text with `@name` binds; rows are scanned by the existing
  `scanProvider`/`scanUser` through `momo.RawRows`, and `queryRow` returns
  `sql.ErrNoRows`, so `mapErr`'s ErrNotFound is unchanged. No bind is
  reused, so no cast was added.
- **Transactions and errors unchanged.** SaveProvider, SetSSORequired
  (the `FOR UPDATE` count of enabled break-glass admins, then the update),
  Link, Unlink and CreateUser run in `momo.DB.Tx` with their audit rows. A
  unique violation of the identity or the username is still ErrLinked
  (`momo.ErrUniqueViolation` or the 23505 `*pgconn.PgError` check it
  always made), and a refused CreateUser rolls back the user with its
  identity. Link and CreateUser now share the identity insert
  (`insertIdentity`); the statement is the one both ran.
- **Tests.** `TestPGStatementsBuild` runs every method on a scripted momo
  driver (the same as the directory package's) on the PR tier.
  `TestStoreContractMem`/`PG` run the same provider, policy, user-lookup,
  identity and just-in-time assertions on Mem and PG; the known differences
  are in the test's comment.

## Queue failover flake: an agent bridged meanwhile was rung again (2026-11-16, fix/queue-failover-flake)

- **Product bug, not test timing.** TestQueueSurvivesNodeFailover failed once
  in CI (run 43861480723, attempt 1, branch momo/webhook, which does not
  touch internal/sip) with `waiting after the first answer = []`: after the
  survivor bridged caller 100 to agent 200, caller 210 had left the queue
  too. 0 failures in 50 local `-race` runs of the test and 0 in 100 runs of
  an instrumented build, which logged every queue Leave and call end: the
  dead owner never removes an entry (its flows return on `s.done`, or yield
  with `haYielded` set), so the only way out for 210 was being answered.
- **Cause.** `queueOffer` reads the free agents (`queueAgents`, which skips
  busy ones) and only then takes their leases. `NextInLine` lets 210 make
  an offer while the agent rings for 100 (100's `Offering` is set), so 210
  can read 200 as free before 100's 2xx. 100's flow keeps 200's lease until
  the bridge is up and the agent shows busy (`release()` after
  `ivrConnect`). If 210's offer stalls between the read and the lease
  request across that window, it gets the lease and rings an agent who is on
  a call. In the test the agent auto-answers, so 210 is answered and leaves.
  The same race exists across nodes in production: an agent gets a second
  queue call while on the first.
- **Fix.** `queueStillFree`: once the leases are held, read the free agents
  again and release the leases of agents that are no longer free. The winner
  releases its lease only after the agent shows busy (its call has a winner
  locally and the live call is published as connected), so a lease taken
  after that release always sees the agent busy. One extra agents/stats read
  per offer round that leased an agent.
- **Deterministic reproduction.** A test hook `queueFreeHook` runs after an
  offer finds free agents and before it takes the leases.
  TestQueueAgentBridgedMeanwhileNotOfferedAgain stalls the second caller
  there until the first is bridged and the agent's lease is released. Before
  the fix it failed 3/3 (the agent got a second INVITE). With the fix it
  passed 20/20. A temporary copy of the failover test with the same stall
  failed 3/3 with the CI message `waiting after the first answer = []`, and
  passed with the fix. The copy was not kept. The failover test is
  unchanged. With the fix it passed 50/50 under `-race`.

## momo Phase 3: the internal/store seam (2026-11-16, momo/store-seam)

- **Scope: the seam only.** `internal/store` (81 files, about 1,400 call
  sites) runs every statement on momo v0.14.0 through one file,
  `internal/store/dbq.go`; the SQL text of every statement is unchanged and
  keeps its `$n` binds. Converting files to `@name`, `momo.Query[T]` or model
  writes is left to the per-domain PRs that follow. No schema change, no
  migration, `go.mod` unchanged.
- **Why `$n` can stay.** momo's raw path binds native `$n` on PostgreSQL as
  positional argument n (`raw_args.go`, `appendRawDollar`), renumbering the
  binds in order of first use, so the SQL PostgreSQL receives means the same.
  One style per statement: a `$n` statement with a `?` or `@name` beside it,
  a `$n` past the arguments and an argument no `$n` uses are
  `momo.ErrValidation` at build. `TestStatementsBuild` reads the package's
  source and builds every statement passed as a constant (1,326) on
  `momo.NewOffline` with the call site's argument count; all build. 81 are
  assembled at run time and are left to the DB-backed tests.
- **How the seam keeps 1,400 call sites unchanged.** `dbq` is now an
  interface of `momo.Querier` plus `database/sql`'s three query methods,
  returning the store's `*dbRows`/`*dbRow`. `*dbPool` (the `*momo.DB` from
  `internal/db.New` plus the `*sql.DB`) and `*dbTx` (momo's view of an open
  `*sql.Tx` from `db.AdoptTx`, plus the `*sql.Tx`) implement it by embedding
  the `momo.Querier`, so converted code passes them straight to momo.
  `ExecContext` is `momo.Exec` with a rows-affected `sql.Result`;
  `QueryContext` is `momo.RawRows`, whose Scan is `database/sql`'s, so NULL
  handling and conversions are unchanged; `QueryRowContext` keeps
  `*sql.Row`'s contract (error deferred to Scan, `sql.ErrNoRows` on no row,
  first row only, Close's error reported). The change is a type swap:
  `*sql.Tx` became `*dbTx` in 448 store signatures, `*sql.Rows`/`*sql.Row`
  became `*dbRows`/`*dbRow` in the local query interfaces, and the store's
  `db` field became `*dbPool`.
- **Construction.** `store.New(db, log)`: the store builds its momo handle
  over db with `internal/db.New(db, log)`, so the handle and the
  `*sql.DB` that begins the transactions cannot be two databases. A nil log
  discards. hello-control and hello-sip pass their telemetry logger; tests
  pass nil.
- **Transactions.** `BeginTx` still begins a `database/sql` transaction and
  adopts it; the store commits or rolls back as before (momo never does).
  So `configChangeID` (the `0x68656c6c6f636667` lock, baseline check,
  change, audit, revision bump and `pg_notify` in one transaction) and
  `RunBatch` (the context-carried batch transaction) are unchanged code on
  the adopted transaction; the lock and NOTIFY statements are the same SQL.
  `DialNumbersTaken` and `ProvisionDirectoryExtension` keep their `*sql.Tx`
  parameter (the directory Provisioner passes the transaction the directory
  store began through momo, as `pgxdriver.UnwrapSQLTx`) and adopt it.
- **Raw on purpose.** BEGIN; `TryLock`'s pinned connection, whose session
  holds the advisory lock; and `SchemaVersion`'s read of `goose_db_version`,
  which goose owns until Phase 5.
- **Prepared insert.** `InsertFetches` prepared its insert once per
  transaction; it now runs the same statement text per row in the same
  transaction (momo has no Prepare on raw SQL). pgx's per-connection
  statement cache still prepares it once.
- **mapErr, rewritten once.** A unique violation (`momo.ErrUniqueViolation`
  or SQLSTATE 23505) is `ErrConflict: <constraint>`; a foreign-key violation
  (`momo.ErrForeignKeyViolation` or SQLSTATE 23503) is
  `ErrNotFound: <constraint>`, or the `InUseError` when the
  `*pgconn.PgError` message starts "update or delete on table" (momo does
  not expose the message, so this stays the pgconn fallback); everything
  else is returned as is. The constraint comes from `momo.Error.Constraint`,
  else the `*pgconn.PgError`. `TestMapErr` pins both forms.
- **Errors callers see.** Errors that mapErr does not translate now read
  `momo: <kind> (op=...): <driver error>` (a check violation still names its
  constraint); `errors.Is`/`errors.As` still reach `sql.ErrNoRows`,
  `sql.ErrTxDone`, the context errors and the `*pgconn.PgError`.
  `queueFK`, `ivrFK`, `blocklistFK` and `pagingFK` keep working through
  `errors.As`. Five `err == sql.ErrNoRows` became `errors.Is` (errorlint
  only exempts `*sql.Row.Scan`; the seam returns the bare sentinel, so
  both forms were already equal).
- **Cost.** `BenchmarkSeam` over a driver that does no I/O: about 0.75 us
  and 14 allocations per statement for momo's build, hooks and cursor, the
  same with the production logger at info level. A PostgreSQL round trip is
  100 us or more, so the layer is under 1% on hello-sip's per-call store
  reads (DeviceSecret, time conditions, settings, queue callbacks).
  hello-sip's configuration snapshot (`internal/snapshot.Load`) does not go
  through the store; it is unchanged.

## momo Phase 3, domain 1: internal/store core and configuration on @name (2026-11-16, momo/store-core)

- **Scope.** `store.go`, `batch.go`, `config.go`, `dialspace.go`,
  `dirprovision.go` and `flow_canvas.go` in `internal/store` (77 calls)
  bind by name: every `$n` became `@name` with `momo.Named`. Call sites keep
  going through the seam (`ExecContext`, `QueryContext`, `QueryRowContext`
  on `s.q(ctx)`, `s.db` or the `*dbTx`) with their Scan code; the SQL text
  is otherwise unchanged. No schema change, no migration, `go.mod` and
  `internal/db` unchanged.
- **Reused binds carry the old type.** momo renders one bind per use of an
  `@name`, where PostgreSQL typed the old single `$n` once. Each use that
  could type differently is cast to the type the `$n` had:
  ListConfigBatches `@cutoff::timestamptz` (both uses), ListDevices
  `@kind::text` (both uses; `kind` is TEXT, and the `@kind = ''` use alone
  would otherwise be untyped), UpdateExtension `@dnd::boolean` (both uses).
  The token lookup's three `@hash` uses and RotateDeviceSecret's two `@id`
  uses compare with bytea and bigint columns, so need none.
- **Lock key and NOTIFY unchanged.** The configuration lock and the revision
  NOTIFY moved into `lockConfig` and `notifyConfig` (used by configChangeID,
  RunBatch and bumpConfigRevision), which send exactly
  `SELECT pg_advisory_xact_lock($1)` with `0x68656c6c6f636667` and
  `SELECT pg_notify($1, $2::text)` with `hello_config` and the revision;
  `TestConfigLockAndNotify` pins both on a recording driver.
- **boundPhone (prov.go) binds `@id`.** Its only callers are DeleteExtension
  and DeleteDevice, whose WHERE fragments are in config.go; the fragment and
  the bind move together, so prov.go's one statement changed here, ahead of
  the provisioning domain.
- **Shared helper `one` (dbq.go).** The first row of a `momo.Query` or
  `momo.Scalars` read, or `sql.ErrNoRows` for none, so `mapErr` and the
  no-row checks behave as with `QueryRowContext`. ConfigRevision uses it
  (`momo.Scalars[int64]`); `TestOne` pins it.
- **TestStatementsBuild learned momo.Named.** It builds every constant
  statement passed to the seam or to `momo.Exec`, `momo.Query`,
  `momo.Scalars` and `momo.RawRows`, with a `momo.Named(name, nil)` for each
  `momo.Named` argument of the call site (one Named per distinct `@name` for
  a call that spreads a slice), so a misspelled or unused name fails on the
  pull request tier. `namedFiles` lists the converted files; a `$n` left in
  one of them fails the test. The seam file itself is no longer swept (its
  three momo calls are its callers' statements). 1,326 constant statements
  build, 60 of them with `@name` binds; 81 are assembled at run time.
- **Recipe for domains 2 to 13** is in `docs/database.md` ("Converting an
  internal/store file to @name"), which also lists every package on momo
  now.

## momo Phase 3, domain 4: internal/store phonebook and contacts on @name (2026-11-16, momo/store-phonebook)

- **Scope.** `phonebook.go`, `phonebook_lookup.go`, `personal_contacts.go`
  and `locations.go` in `internal/store` bind by name: every `$n` became
  `@name` with `momo.Named`, through the seam, with the Scan code, locks
  (`FOR UPDATE`, `FOR SHARE`), error handling and SQL text otherwise
  unchanged. No schema change, no migration, `go.mod` and `internal/db`
  unchanged.
- **Caller-name lookup keeps its plans.** `phonebook_lookup.go`'s six
  statements use each bind once, so momo renders the same SQL with the same
  `$1`, `$2` in the same order and PostgreSQL infers the same parameter
  types; no cast was needed or added.
- **No casts added in this domain.** The two reused binds compare with
  columns of one type on every use: `requireDialTarget`'s `@number`
  (`extensions.number` and `ring_groups.name`, both TEXT) and
  `listAssignments`' `@location_id` fragment, appended to three SELECTs
  whose `location_id` columns are all BIGINT.
- **Run-time statements.** `listAssignments` passes its
  `momo.Named("location_id", ...)` only together with its WHERE fragment,
  `WHERE location_id = @location_id`. `writeLocation` builds its Named slice once and
  spreads it into the INSERT and, with `momo.Named("id", id)` appended, the
  UPDATE. `SetLocationAssignment`'s `UPDATE <table>` binds `@location_id`
  and `@id`.
- **Book lock key unchanged.** `CreatePhonebook` still sends
  `SELECT pg_advisory_xact_lock(@key)` with `int64(bookLockKey)`, rendered
  as one bigint bind as before.

## momo Phase 3, domain 5: internal/store security and door on @name (2026-11-16, momo/store-door)

- **Scope.** `scep.go`, `wifi.go`, `door.go`, `door_open.go`,
  `door_camera.go` and `door_webhook.go` in `internal/store` bind by name:
  every `$n` became `@name` with `momo.Named`, through the seam with the
  Scan code, `FOR UPDATE`, `mapErr` and the no-row checks as they were. No
  schema change, no migration.
- **Secrets unchanged.** The sealed values (the SCEP CA key, the challenges,
  the 802.1X and Wi-Fi secrets, the door-open code, the camera password, the
  webhook secret) are sealed, bound and opened as before, with the same
  additional data; only the bind is by name. Nothing logs an argument value
  (a build error names the statement and the bind, not its value).
- **door_open is the configuration of the DTMF `*50` path.** Its statements keep
  their meaning and parameter types (bigint id, bytea code, boolean). The
  SIP node reads the codes through `internal/snapshot`, which is unchanged.
- **Casts.** ListSCEPCertificates casts both uses of `@phone_id` to bigint,
  as the old `$1::bigint` typed its single bind. SetDoorCamera's upsert
  casts the `ELSE @password_enc::bytea` use in its CASE; the other reused
  binds there (`@kind`, `@url`, `@username`, `@password_enc` in VALUES) and
  wifiInfo's two `@phone_id` uses are typed by their columns.
- **Run-time statements bind by name too.** UpdateDot1XProfile appends
  `@password_enc` with its fragment only; memberOf's four statements bind
  `@extension_id`; sealWifi's statement (`wifiSetPSK`) binds `@id` and
  `@enc`; replaceWifiAssignments' pair binds `@owner` and `@profile_id`.
  These, the `wifiIDs` reads and ListSCEPCertificates are assembled or
  passed at run time, so the DB tier checks them, not
  `TestStatementsBuild`.

## momo Phase 3, domain 3: internal/store faxes, mail and backup on @name (2026-11-16, momo/store-fax-mail)

- **Scope.** `faxes.go`, `mail.go` and `backup.go` in `internal/store` bind
  by name: every `$n` became `@name` with `momo.Named`, through the seam,
  with the SQL text, `FOR UPDATE` / `SKIP LOCKED`, Scan targets and error
  handling unchanged. No schema change, no migration, `go.mod` and
  `internal/db` unchanged.
- **Reused binds carry the old type.** fax Finish casts `@object_key::text`
  in `NULLIF(...)` and `@object_key::text <> ''`, and `@mail_to::text <> ''`
  (its `mail_to = @mail_to` use takes the column's type). AdoptArchive's
  `INSERT ... SELECT` casts every select-list use to its column's type,
  `@at::timestamptz` (twice), `@archive_bytes::bigint` and
  `@object_key::text`, and `@object_key::text` in its `NOT EXISTS`, since a
  select-list bind cannot type itself once it is a bind of its own. The
  `@name` uses in DeleteFaxDestination's IVR read compare with TEXT columns,
  and the upserts' (PutSMTPSettings, PutBackupSettings) repeated uses are
  each a column's value; the sealed bytes and the clear flag were already
  cast (`::bytea`, `::boolean`).
- **ListFaxes' assembled WHERE.** Each filter condition carries its own
  `@name` (`@direction`, `@status`, `@before`, `@owners`) and its Named goes
  in only with it; the limit is `@limit`. The statement PostgreSQL receives
  is the one it received before (`TestListFaxesSends` pins it, values in
  order). The `fmt.Sprintf` of placeholder numbers, and its gosec
  suppression, are gone.
- **DeleteFaxDestination's in-use reads** are now the constants
  `faxDestRoutes` and `faxDestMenus`, with the same text, so
  `TestFaxStatementsOffline` builds them (the sweep cannot see a statement
  taken from a struct field) along with every ListFaxes filter combination.
- **BumpRevision's NOTIFY** goes through `notifyConfig`, which sends the
  same `SELECT pg_notify($1, $2::text)` with `hello_config` and the revision
  as the code did inline.
- **SchemaVersion stays raw** (`s.db.raw`): goose owns `goose_db_version`
  until the migration engine moves (Phase 5).

## momo Phase 3, domain 2: internal/store users and authentication on @name (2026-11-16, momo/store-users)

- **Scope.** `users.go`, `totp.go`, `oauth.go`, `ldap.go`, `onboarding.go` and `sidekey.go` in `internal/store` (130 calls) bind by name, following the recipe in `docs/database.md`. Call sites, Scan code, error mapping (`mapErr`, `oauthErr`, the `sql.ErrNoRows` checks), `FOR UPDATE` locks and the SQL text are otherwise unchanged. No schema change, no migration; `go.mod` and `internal/db` unchanged.
- **Authentication paths kept as they were.** Token, code and secret hashes are bound as before (the same `[]byte` values); the last-admin, last-break-glass and last-enrolled-admin checks, the MFA throttling (5 attempts per challenge, 10 failures per user per 10 minutes) and every audit write stay in the same transaction as the change they record.
- **Casts added for reused binds.** UpdateAccount `@password_hash::text` on its COALESCE use (the other use was already cast; `password_hash` is TEXT). CaptureCandidate's refresh `@vendor::text` on both uses (`vendor` is TEXT; the `<> 'generic'` use alone would type it from a literal). Reused binds that already had a cast (`@before::bigint`, `@user_id::bigint`, `@ttl::float8`) keep it on every use. Reused binds compared with columns of one type on each side need none: revokeClientDevices `@user_id`, CaptureCandidate `@mac` (TEXT in phones, onboarding_candidates and onboarding_ignored), PruneOAuth `@cutoff` (timestamptz columns).
- **revokeGrants takes a caller's fragment that binds by name.** Its callers pass the fragment's `momo.Named` values with it (`id = @id`; `id = @id AND (@user_id::bigint = 0 OR ...)`; the reused-code fragment binds `@user_id`, `@client_id` and `@created_at`). The token update it runs binds `@ids`, so a fragment must not use that name.
- **The onboarding advisory lock is unchanged.** `SELECT pg_advisory_xact_lock(@key)` with `onboardLockKey` (`0x68656c6c6f6f6e62`) reaches PostgreSQL as before.
- **Hermetic test for the statements assembled at run time.** `TestUsersDynamicStatements` runs UserExtensions, UserExtension, UserVoicemailMessage, UserCDRs, PruneOAuth and revokeGrants (with each caller's fragment) on the recording fake driver, so a misspelled name in them fails on the pull request tier too. TestStatementsBuild builds 1,323 constant statements, 169 of them with `@name` binds.

## momo Phase 3, domain 7: internal/store call features on @name (2026-11-16, momo/store-callfeatures)

- **Scope.** `ivr.go`, `queues.go`, `queue_callbacks.go`, `followme.go`,
  `pickup.go`, `park.go`, `paging.go`, `ringmap.go`, `ringtag.go`,
  `conference.go` and `multicast.go` in `internal/store` bind by name:
  every `$n` became `@name` with `momo.Named`, through the seam, with the
  Scan code, locks, error handling and SQL text otherwise unchanged. No
  schema change, no migration, `go.mod` and `internal/db` unchanged.
  `prov_multicast.go` belongs to the provisioning domain and is not part of
  this change.
- **hello-sip's queue callback claims unchanged.** `queue_callbacks.go`
  keeps `SELECT 1 FROM queues WHERE id = @id FOR UPDATE` (Request) and the
  Claim's `FOR UPDATE SKIP LOCKED LIMIT 1` sub-select. Its statements on
  the call path (settle's two UPDATEs, Request, Head, Claim, Renew, Fail,
  Connect, Due, Dialling, Purge) keep their parameter types: every bind is
  typed by its column or by `make_interval(secs => ...)` as before.
  `callbackDue` binds `@queue_id`. Fail's `@max_attempts` is used twice,
  each against `attempts` (INTEGER), so needs no cast.
- **One cast added.** `catalogChannels` (multicast.go) uses `@kind` twice:
  `@kind::text = '' OR kind = @kind::text`, the type the old `$1` got from
  `$1 = ''`. Every other reused bind compares with columns of one type on
  each use (`@name` in queueReferences and ivrReferences, `@target` in
  queueTextReferences and refuseIfIVRPoints, `@kind`/`@kind2` there, `@id`
  in channelUsers); ringmap's `@group_id::bigint` was cast already.
- **Run-time statements.** `listQueues`, `listIVRMenus`, `listPagingGroups`
  and `listPickupGroups` take a WHERE fragment with its `momo.Named`
  (`WHERE ... id = @id` with `momo.Named("id", id)`). The queue INSERT and
  UPDATE build their placeholder and SET lists from `queueWriteCols`, each
  column binding as `@<column>`, and `QueueInput.args` returns one
  `momo.Named` per column. The IVR destination check, the follow-me target
  check, the paging list deletes, ring tag usage reads and
  `channelUsers` pick a statement at run time; each candidate binds the
  same name (`@target`, `@group_id`, `@name`, `@id`).
  `setConferencePIN`'s `UPDATE ... SET <col>` binds `@sealed` and `@id`.
