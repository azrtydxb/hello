# voice-agents — implementation plan

Status: approved (spec open questions answered 2026-10-08)
Spec: .procoder/specs/voice-agents.md

## Goal

An operator creates a voice agent in Hello's console, gives it a persona and MCP tools, and routes an extension, a DID or a ring group's last resort to it. A call reaches the separate talking-agent service over SIP with a signed header set, the persona and tools come from Hello's runtime API with live reload, and the CDR names the agent and carries the call summary. Hello's media path is unchanged. A fake talking-agent in the lab proves it end to end; `TestKwSmokeVoice` proves it on kw once talking-agent's changes land.

## Architecture

Inside hello-control, beside the API and the AI service, except routing and the call leg, which are hello-sip's:

- **`internal/voice`**: the registry (agents, versions, MCP servers, attachments, revision counter), the egress-guarded MCP client for discovery and test, the SIP call signature (`Sign`/`Verify`), the runtime view builder and ETag/long-poll, ack and report stores, prune and metrics.
- **`internal/routing`**: new destination kind `voice_agent`, compiled from `Config.VoiceAgents` (name, sip user, enabled) and the voice SIP address, into the existing `KindInbound` decision with a `SIPURI` plus the `VoiceAgent` field for the CDR and header set.
- **`internal/sip`** (hello-sip): the existing `ringURI` leg adds the S-15 headers and signature when the decision carries a voice agent, offers G.711 only, and records the agent on the CDR; it refuses the agent address as a caller.
- **`internal/api`**: `/api/v1/voice/...` and `/api/v1/voice-runtime/...` handlers, thin: auth, decode, call `internal/voice`. A new `voice-runtime` scope in `internal/auth`, service-account only.
- **`web/`**: four new pages and the routing pickers.
- **`test/fakeagent`** (new): the fake talking-agent UA; **`test/fakemcp`** (new): a fake MCP server.

Why talking-agent stays outside, why the runtime pulls (long poll) instead of Hello pushing, and why the call is authenticated by HMAC in a SIP header: spec Problem, S-16, S-19 and the talking-agent section; Hello is the system of record for numbers, personas and credentials, and talking-agent can sit behind NAT or a masking L2 forwarder where source IPs mean nothing.

## Constraints

- No new direct Go dependency; no database or service on a developer machine (PostgreSQL, Valkey, SIP tests run in CI and the lab).
- Hello's hot path changes only as S-15 describes; routing stays pure.
- With `HELLO_VOICE_SIP_ADDRESS` unset, routes to agents fail validation (`voice_not_configured`) and nothing else changes.
- Each task leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...` (test databases set), `procoder check` 0 blocking, and `procoder test`/`procoder lint` over `web/` (where touched) green; safety branches (credential never returned, signature verification, egress guard, scope check, role gate) are mutation-checked with a snapshot taken immediately before each mutation.
- The talking-agent changes are a separate repository's work; Hello's tasks use the fake agent and a signature vector file shared by both repos.

### Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Schema:** `migrations/00010_voice_agents.sql` with its down migration, as the spec's Data section lists it, `CHECK` lists on every enum and range.
2. **Config** (`internal/config`): `Voice{SIPAddress, Secret, SecretNext, Tenant string; MaxAgents, MaxCalls int; AllowPublicMCP, AllowLoopback bool}` with validation (host:port, key length at least 32 bytes), env names of the spec.
3. **Routing input:** `routing.Config.VoiceAgents []VoiceAgent{Name, SIPUser string; Enabled bool}` and `routing.Config.VoiceSIPAddress string`; `Decision.VoiceAgent *VoiceRef{Name, SIPUser string}`; destination kind `voice_agent` in inbound routes, ring group members and failure targets.
4. **Signature** (`internal/voice`): `Sign(key Key, tenant, agent, sipUser, correlation string, ts time.Time) string` (the origin header `X-Hello-Caller-Origin` is sent but not signed input; the signed string stays as S-16) and `Verify(keys []Key, header string, now time.Time, ...)`; vectors in `internal/voice/testdata/voice_auth_vectors.json`, copied to talking-agent's repo.
5. **Runtime view:** `voice.View{Tenant string; Revision int64; Agents []RuntimeAgent}`, ETag = `W/"v<revision>"`-free strong tag `"<revision>"`; the unsealed credentials exist only in `voice.RuntimeView(ctx)`.
6. **Auth:** `auth.ScopeVoiceRuntime = "voice-runtime"`, excluded from `GrantableScopes` and from consent; a service account may hold it alone.
7. **Routes** (`internal/api/routes.go`): every operation of the spec's Interfaces table in `routes()` with its scope and role answering `501` through `s.pending` until its stream lands, and in `openapi.json` with descriptions, `x-hello-*` and schemas, so phase 1's route/document tests stay green from the first merge.

Deviations recorded when Task 1 landed (all compatible with Tasks 2-7; ask the lead before relying on more):

- `voice_revision` is its own single-row table (`voice_revision(id, revision)`, seeded 0), not a column of `voice_runtime`: the runtime singleton records what talking-agent last read (revision, last_seen_at, loaded, version, service account), while the counter is bumped once per registry change in its own transaction.
- `ring_group_members`' old primary key `(group_id, extension_id)` cannot span a nullable column; the up migration drops it for two unique constraints, `(group_id, extension_id)` and `(group_id, voice_agent_id)`, with `CHECK (num_nonnulls(extension_id, voice_agent_id) = 1)`; the down migration restores the primary key.
- The three `voice-runtime` operations carry minimum role `viewer` (the scope alone gates them; TestRoutesTable's scope→role map now lists `voice-runtime → viewer`).
- `X-Hello-Auth` key ids are derived (`hex(sha256("hello-voice-key-id|"+secret))[:4]`), so the vectors file needs no separate id column and both sides derive the same id from a shared secret; `TestVoiceCallAuth` proves the derivation.
- Task 1 landed on branch `voice-contracts` (the plan's heading says `voice-agents-contracts`; the lead's stream name won).

## Task 1: Shared contracts (lead, branch voice-agents-contracts)

Files: `migrations/00010_voice_agents.sql`, `internal/config/`, `internal/auth/scope.go`, `internal/routing/types.go`, `internal/voice/sign.go` and vectors, `internal/api/routes.go`, `internal/api/openapi.json`.
Interfaces: produces contracts 1-7.

- [x] Migration up and down; `go test -run Migrate ./test/integration/` has `TestMigrateVoiceAgentsRollback`, passing.
- [x] Config keys with defaults and validation; `TestLoadVoice*`, plus `TestVoiceDisabledWithoutAddress` (config half).
- [x] `voice-runtime` scope, service-account only; `TestVoiceRuntimeScope` (consent cannot grant it, it grants nothing else).
- [x] `Sign`/`Verify` with vectors, skew, replay input, two keys; `TestVoiceCallAuth` (the `internal/voice` half).
- [x] Route rows and OpenAPI operations (`501` pending); `TestRoutesMatchOpenAPI`, `TestOpenAPIForTools`, `TestRoleEnforcement` green.

## Task 2: Registry, versions, MCP servers (branch voice-agents-registry, after Task 1)

Files: `internal/voice/registry.go`, `versions.go`, `mcp.go`, `egress.go`, `internal/store/voice.go`, `internal/api/voice_agents.go`, `voice_mcp.go` and tests, `test/fakemcp/`.
Interfaces: produces the registry operations, caller verification fields and the `voice_revision` counter; consumes contracts 1, 2, 7 and `internal/secret`.

Deviations recorded when Task 2 landed (all additive; ask the lead before relying on more):

- The generated `sip_user` is numeric (`^[0-9]{3,15}$`, a fixed migration CHECK) instead of the spec's `va-<8 hex>` shape, which the fixed schema cannot store; it still hides the agent's name and extension.
- The OAuth `audience`/`resource` field of S-5 has no column in the fixed migration, so it is not stored; `scope` is sent as documented, and the field needs a migration of its own if the lead wants it.
- The Error schema's `code` enum gained the five voice codes of the spec's Interfaces table (Task 1's document omitted them), `VoiceAgentInput` gained `extension` (S-3 is unsettable otherwise), and the three voice GET operations gained their documented `403` (phase 1's role-enforcement probe answers it now that the routes stopped being pending).
- `api.Config` gained the `Voice` registry field and `cmd/hello-control/main.go` wires `voice.New(st, box, cfg.Voice, log)` — one line each, the same plumbing phase 2 used for its services.
- Registry reads that are not tool traffic (the agents/servers lists) go through the same configChange machinery, so an agent change also bumps the configuration revision and notifies hello-sip, which Task 4's compiled table wants.

- [x] Agent CRUD, limits, generated `sip_user`, extension namespace uniqueness, 10 versions, restore, references on delete (`409`), audit rows without text; `TestVoiceAgentCRUD`.
- [x] Caller verification (S-36): modes, allowlist, salted PIN hash, `voice_verification_required` on write tools without it; `TestVoiceCallerVerification`.
- [x] MCP servers: bearer, header and OAuth client-credentials auth, sealed credential, write-only, admin-only credential, attachments with allowlists and `confirm` defaults, tool name conflicts; `TestVoiceMCPServers`.
- [x] Guarded dialer (token URL too), OAuth client-credentials exchange, discovery and test through the Go SDK client against `test/fakemcp`; `TestVoiceMCPDiscovery`.
- [x] Every change bumps `voice_revision` in its own transaction; covered inside `TestVoiceAgentCRUD` and `TestVoiceMCPServers` (store half).
- [x] Mutation-check the credential response filter, the egress check and the admin-only gate. Full gate green except the database-backed tests, which run in CI (no database on a developer machine).

## Task 3: Routing and ring groups (branch voice-agents-routing, after Task 1; parallel with Task 2)

Files: `internal/routing/` (types, engine, decide, tests), `internal/store/routing.go` and ring group code, `internal/api` route and ring group handlers and validation, `migrations` follows contract 1.
Interfaces: produces `voice_agent` as destination everywhere of S-10 to S-13; consumes contracts 1, 3.

- [ ] Compile and validate `voice_agent` for inbound routes and internal extension resolution; trace line; `TestVoiceAgentRouting`.
- [ ] Ring group member and failure target, `sequential`-only restriction, XOR constraint; `TestVoiceAgentRingGroup` (routing and API halves).
- [ ] Routing test endpoint shows the step; transfer and feature code reach the agent extension.
- [ ] Mutation-check the strategy restriction and the disabled-agent validation. Full gate.

## Task 4: The call leg (branch voice-agents-sip, after Tasks 1 and 3)

Files: `internal/sip/callflow.go` and `b2bua.go` (the `ringURI` leg), `internal/sip/voice.go`, `internal/cdr/`, `internal/store/cdr.go`, `test/sipua` (fake agent behaviours), tests.
Interfaces: produces the signed INVITE and the CDR fields; consumes contracts 2, 3, 4.

- [ ] Headers, signature, G.711 offer, stripped caller headers, ring timeout, response mapping; `TestVoiceAgentInvite` against `test/sipua`.
- [ ] Refuse the agent address as a caller; covered by `TestVoiceAgentInvite`.
- [ ] Capacity (S-35): per-agent and total caps counted like trunk `max_calls`, busy to the next step or failover, `486` mapped the same, `X-Hello-Caller-Origin` header; `TestVoiceCapacity`.
- [ ] CDR columns written and filter `voiceAgent` on `listCDRs`; `TestVoiceCDRFields` (integration, in Task 8 if databases are needed).
- [ ] Metrics `hello_voice_*` for setup and unreachable; `TestVoiceMetrics` (SIP half).
- [ ] Mutation-check the header strip and the signature input. Full gate; media tests unchanged and green.

## Task 5: Runtime API, ack and call reports (branch voice-agents-runtime, after Tasks 1 and 2)

Files: `internal/voice/runtime.go`, `report.go`, `prune.go`, `internal/api/voice_runtime.go` and tests.
Interfaces: produces the three runtime operations and the status; consumes contracts 5, 6.

- [ ] View builder with unsealed credentials, ETag, `304`, long poll that wakes on a revision change (Postgres `LISTEN`/`NOTIFY` or a 500 ms check; the simpler that passes the 2 s criterion); `TestVoiceRuntimeAPI`.
- [ ] Ack, status states, rate limit, red after 2 minutes; `TestVoiceRuntimeStatus` (includes S-22).
- [ ] Report: idempotent, early reports, size limits, transcript only when allowed, CDR detail join; `TestVoiceCallReport`.
- [ ] Prune with advisory lock; `TestVoicePrune`.
- [ ] Not reachable from MCP, the assistant or proposals; `TestVoiceOperationsMCP`; metrics and logs; `TestVoiceMetrics`, `TestNoSecretsInLogs` extended.
- [ ] Mutation-check the scope gate, the transcript flag and the credential presence. Full gate.

## Task 6: Console (branch voice-agents-ui, after Tasks 2, 3 and 5 contracts are mocked)

Files: `web/src/pages/VoiceAgents.tsx`, `VoiceAgentEdit.tsx`, `VoiceMCPServers.tsx`, `VoiceRuntime.tsx`, their tests, the inbound route, ring group and extension pickers, dashboard card.
Interfaces: consumes the routes of the spec's Interfaces table.

- [ ] Agents list and wizard, editor tabs with versions, tools checklist and warnings; `VoiceAgents.test.tsx`, `VoiceAgentEdit.test.tsx`.
- [ ] MCP servers page, role-aware credential field, plain-text discovery output; `VoiceMCPServers.test.tsx`.
- [ ] Routing pickers and trace; `VoiceRouting.test.tsx`; Test tab following the next CDR; runtime page and card.
- [ ] `procoder test` and `procoder lint` over `web/` green.

## Task 7: Fake talking-agent and end to end (lead, after Tasks 2-5)

Files: `test/fakeagent/`, `test/integration/lab_voice_test.go`, `test/integration` CDR and logs tests.
Interfaces: consumes everything above.

- [ ] `test/fakeagent`: SIP UA verifying `X-Hello-Auth` with the shared vectors, answering with a tone, polling the runtime API with a service account and reloading within 2 s, posting reports.
- [ ] `TestVoiceAgentsEndToEnd` in the lab (docker compose on the CI runner, never on a developer Mac): create agent and route through the API, call from a fake phone, persona edit propagates, bad signature refused, ring group failure target, disabled agent, CDR with report.
- [ ] `TestVoiceCDRFields` and `TestNoSecretsInLogs` extension run against that stack.

## Task 8: kw, docs and release (lead, after Task 7 and after talking-agent items 1-6 land)

Files: `deploy/kuvryn-sync/kw/resources.yaml` (hello-control and hello-sip env, optional Secret `hello-voice`), `deploy/kuvryn-sync/kw/secret-hello-voice.sops.yaml`, `docs/voice-agents.md`, README link, `test/deploy/voice_test.go`, `test/integration/kw_voice_test.go`.
Interfaces: consumes everything above.

- [ ] kw manifest and `TestKwVoiceAgents`; confirm with the cluster that `talking-agent-sip` is `192.168.10.142` and that its source ranges and `SIP_TRUSTED_FORWARDERS` include hello-sip's address.
- [ ] `docs/voice-agents.md` with generated operation and limits tables and `TestDocsVoiceAgents`.
- [ ] After merge: pin images, Sync to kw, create the runtime service account and Secret `hello-voice`, configure talking-agent's tenant, run `TestKwSmokeVoice` (`HELLO_KW_SMOKE=1`) from the Arc runner, call a test agent from a real phone.

## talking-agent work (tracked in its repo; not Hello tasks)

The twelve items of the spec's "Required changes in talking-agent". Items 1-6 gate Task 8's smoke test; 7-9 gate acceptable call quality; 10-12 are independent.

## Acceptance criteria

See `.procoder/specs/voice-agents.md`: each criterion cites its named test; `TestVoiceAgentsEndToEnd` is the lab proof, and `TestKwSmokeVoice` plus one real phone call on kw close the epic.
