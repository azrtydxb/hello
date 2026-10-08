# ai-agent — implementation plan

Status: draft (open questions answered 2026-10-08)
Spec: .procoder/specs/ai-agent.md

## Goal

With `HELLO_AI_BASE_URL` and `HELLO_AI_MODEL` pointing at kw's fastllm, an operator asks the console assistant a question about the PBX and gets an answer grounded in read-only tool calls replayed as them, plus a validated proposal shown as a diff that they apply through Hello's own API; deterministic detectors raise PBX findings (REGISTER floods, trunk failures and answer-seizure drops, node flaps, capacity, config smells) that the model explains and ranks — and nothing in Hello changes without a human apply.

## Architecture

All inside hello-control, beside the API, except call quality (Task 5), which hello-sip records into the CDR:

- **`internal/replay`** is phase 1's in-process replay (`internal/mcp/replay.go`) moved out so MCP, the assistant and proposal apply share one function: `Do(ctx, api http.Handler, Request) Result`, copying only the headers the caller passes, refusing nested replays, with phase 1's timeout, body cap and panic recovery; `Redact(v, op)` withholds `x-hello-secret` values. MCP keeps its behaviour and tests.
- **`internal/ai`** is the bounded service (provider over go-ai-sdk, privacy dialer, limits, usage and budget, `Generate` with validator retry, `DataBlock`), tasks with Valkey heartbeats, the scheduler with advisory locks, prune and metrics.
- **`internal/ai/proposal`**: allowlist, validation against `apispec`, before/after, fingerprint, store, apply and dismiss.
- **`internal/ai/assistant`**: sessions, messages, the read-tool adapter (`apispec` operation → go-ai-sdk tool, executed by `replay.Do` under an agent identity), and the message task.
- **`internal/ai/detect`**: one file per detector, the `aiops` agent, samples, findings lifecycle and the explanation call.
- **`internal/sip` and `internal/cdr`** (hello-sip) keep each anchored call's last relay stats snapshot and write `rtp_packets`, `rtp_lost`, `rtp_jitter_ms` to its CDR (spec S-5.1).
- **`internal/api`** gains the `/api/v1/ai/…` handlers (thin: auth, decode, call the packages); `cmd/hello-control` wires the service when AI is enabled. The console gets four pages.

Why tools for reads and structured output for the answer, and why in hello-control: spec S-6 and S-7; the replay and the users, audit and config tables are hello-control's, and a separate service would need credentials in flight.

## Constraints

- One new direct dependency: `github.com/azrtydxb/go-ai-sdk` `v0.6.0`. Models only in `internal/ai/provider.go`; unit tests use `ai/aitest` mock models (helpers in `internal/ai/aifake`); integration tests a scripted OpenAI-compatible fixture in `test/fakellm` (per-feature response queues, records requests); the kw smoke test the real fastllm.
- No hot-path change: hello-sip, provisioning and config writes are untouched except the replay move.
- With AI unconfigured, nothing starts and the new operations answer `503` `ai_disabled`.
- Each task leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...` (test databases set), `procoder check` 0 blocking, and `procoder test`/`procoder lint` over `web/` (where touched) green; safety branches are mutation-checked (snapshot immediately before, restore immediately after, `cmp`). REVIEW.md applies. CI on the Arc runners; kw via Kuvryn Sync; nothing on the user's Mac.
- Open questions answered: (1) call quality in CDRs (Task 5); (2) never mask on public endpoints (no change); (3) allow deletes of routes and ring groups (Task 3).

### Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Schema:** `migrations/00009_ai_agent.sql` with its down migration, as the spec's Data section lists it, `CHECK` lists on every status, severity, outcome and role column.
2. **Config** (`internal/config`): `Control.AIAgent{Provider, BaseURL, Model, APIKey string; AllowPublic bool; StructuredOutput string; ValidationAttempts, MaxSteps, MaxConcurrency, RequestsPerMinute int; Timeout time.Duration; DailyTokenBudget int64; BackgroundBudgetPercent int; AgentStartDelay, AIOpsInterval, ExplainMinInterval time.Duration}` from the spec's env keys; `Enabled() (bool, reason string)`.
3. **Replay** (`internal/replay`): `type Request struct{ Method, Path string; Query url.Values; Body []byte; Header http.Header }`, `type Result struct{ Status int; Header http.Header; Body []byte; Fail string }`, `Do(ctx, h http.Handler, r Request) (Result, error)`, `Redact(v any, secretPaths []string)`; `ErrNested`.
4. **Agent identity** (`internal/auth`): `type Agent struct{ UserID int64; TaskID string }`, `WithAgent(ctx, Agent)`, `AgentFrom(ctx) (Agent, bool)`; the middleware, for a request without credentials whose context holds an agent, loads the user (`Lookup.UserActor(ctx, id)`), sets `Kind = KindAgent`, `Scopes = {read}`, current `Role`, and `Via = "ai-assistant"`.
5. **AI service** (`internal/ai`): `New(cfg, store Store, vk valkey.Client, reg prometheus.Registerer, log) (*Service, error)`; `Generate`, generic over the output type, taking `(ctx, svc, call)` and returning the decoded value, `Usage` and an error, where the call is `Call{Feature string; Background bool; System string; Data any; Prompt string; Tools []aisdk.Tool; MaxSteps int; Validate func(context.Context, T) error}` (typed by the output); `DataBlock(v any) string`; error codes as `*ai.Error{Code string}`; `Tasks.Start(ctx, kind, userID, sessionID, fn) (taskID, error)`; `type Agent interface{ Name() string; Interval() time.Duration; Run(ctx) (Outcome, error) }`, `Scheduler.Register(Agent)`.
6. **Proposal** (`internal/ai/proposal`): `Action{OperationID string; PathParams map[string]string; Body json.RawMessage; Before, After json.RawMessage}`, `Draft{Source, Title, Rationale string; SessionID, FindingID *uuid; Actions []Action}`, `(*Validator).Validate(ctx, ident Identity, d *Draft) error` (fills `Before`/`After`), `Store.Upsert(ctx, Draft) (id, error)` (dedupe, supersede), `Apply(ctx, id, userHeader http.Header) (Proposal, error)`, `Dismiss(ctx, id, userID, reason, text)`.
7. **Routes** (`internal/api/routes.go`): every operation of the spec's Interfaces section in `routes()` with its scope and role answering `501` through `s.pending` until its stream lands, and in `openapi.json` with descriptions, `x-hello-*` and schemas, so phase 1's sync and conformance tests hold from Task 1 on.

**Deviations recorded by Task 1** (merged contracts; streams build on these, not the lines above where they differ):

- Contract 3: `Request` also has `ClientID` (the OAuth client in the replay marker, empty for the assistant and apply) and `RemoteAddr`; `Do` logs a handler panic through `slog.Default()`; `replay.Withheld`, `Redact` and `Unescape` live in `internal/replay`, and `mcp.Withheld` aliases the constant.
- Contract 4: also `auth.KindAgent`, `auth.ViaAssistant`, `Actor.Via`, and `auth.WithVia(ctx, via)`/`ViaFrom(ctx)`: the middleware sets `Actor.Via` from the context for any actor, and the store's audit via is `ClientID`, else `Via`. Task 3's apply puts `ai-proposal:<id>` in the context with `auth.WithVia`; nothing else in `auth` changes. `Lookup` gained `UserActor`, implemented by `store.Store`. An agent marker is honoured only when the request has no `Authorization` header.
- Contract 5: `internal/ai/ai.go` holds only types, error codes and the `Agent` interface; `New`, `Generate`, `DataBlock`, `Tasks.Start` (`TaskFunc`) and `Scheduler.Register` are Task 2's, with the signatures in the package comment. The `Store` interface is Task 2's to define in `internal/ai` beside `Service` (it may add it to a file it owns). Ids are `string` UUIDs.
- Contract 6: `Validator` and `Store` are interfaces (so Tasks 4 and 6 compile before Task 3 merges); ids are `*string`; `Identity` is an alias of `auth.Agent`. How a finding's proposal gets its read identity is Task 6's decision.
- Contract 7: operation ids are `getAIStatus`, `listAIFindings`, `getAIFinding`, `acknowledgeAIFinding`, `dismissAIFinding`, `listAIProposals`, `getAIProposal`, `applyAIProposal`, `dismissAIProposal`, `listAISessions`, `createAISession`, `getAISession`, `updateAISession`, `deleteAISession`, `postAIMessage`, `getAITask`, `listAIAgents`, `runAIAgent`. The session writes (create, update, delete) are also `x-hello-mcp` exclusions, as apply, dismiss, acknowledge, post and run-now are; the reads are tools. `getAIStatus` answers `200` with `enabled: false` while AI is off (no `503`). A pending route is skipped by the conformance validator and its documented statuses are not required until its handler replaces `s.pending`.
- Schema: task statuses `queued|running|succeeded|failed`; `ai_agent_requests.requested_by` and the finding and proposal actor columns reference `users` `ON DELETE SET NULL`; sessions, tasks and messages cascade with the owner.

## Task 1: Shared contracts (lead, branch ai-agent-contracts)

Files: `migrations/00009_ai_agent.sql`, `internal/config/`, `internal/replay/` (moved), `internal/mcp/replay.go` and `redact.go` (now thin callers), `internal/auth/middleware.go`, `internal/auth/scope.go`, `internal/api/routes.go`, `internal/api/openapi.json`, `internal/ai/ai.go` (types and signatures), `internal/ai/proposal/proposal.go` (types), `go.mod`.
Interfaces: produces contracts 1–7.

- [x] Migration up and down; `go test -run Migrate ./test/integration/` → `TestMigrateAIAgentRollback` passes.
- [x] Move replay to `internal/replay`, MCP calls it; `go test -race ./internal/mcp/ ./internal/replay/` → phase 1's `TestToolReplay` and `TestResourcesAndPrompts` unchanged and green.
- [x] Agent identity in `internal/auth`; `TestAgentIdentity` (the `internal/auth` half: no header sets it, scope is `read` only, deleted user `401`, demotion applies, `via` set).
- [x] Config keys with defaults and validation (provider enum, structured-output enum, percent 1–100, positive bounds); `TestLoadAIAgent*`.
- [x] Route rows and OpenAPI operations for every new endpoint (`501` pending); `TestRoutesMatchOpenAPI`, `TestOpenAPIForTools`, `TestRoleEnforcement` green.
- [x] `go get github.com/azrtydxb/go-ai-sdk@v0.6.0`; `go build ./...`. Commit, PR, merge; streams branch from that main in their own worktrees.

## Task 2: AI core (branch ai-agent-core)

Files: `internal/ai/` (`provider.go`, `privacy.go`, `limits.go`, `usage.go`, `generate.go`, `datablock.go`, `tasks.go`, `heartbeat.go`, `scheduler.go`, `prune.go`, `metrics.go`, `status.go`, tests), `internal/ai/aifake/`, `internal/store/ai_*.go`, `internal/api/ai_status.go`, `cmd/hello-control/main.go` (wiring), `test/fakellm/`.
Interfaces: produces contract 5 and `getAIStatus`, `listAIAgents`, `runAIAgent`, `getAITask`; consumes contracts 1–2.

- [ ] Provider for `openai` and `anthropic` with the privacy dialer (resolve, check every address, dial the checked address); `TestPrivacyGuard` covers literals, `localhost`, mixed public/private answers, rebinding between start and call, and the opt-in.
- [ ] Enablement and `503` `ai_disabled` for every AI operation while off; `TestAIEnablement`.
- [ ] `Service` limits (semaphore, token bucket, timeout, 5 s interactive wait), usage upsert per call, budget with the background share; `TestTasksAndLimits` (limits half).
- [ ] `Generate`: feature line and notice, `Output` object mode or prompt mode, validator retry with the error text, `length` retry, reasoning dropped and counted; `DataBlock` escaping; `TestGenerate` with `aitest` scripts.
- [ ] Tasks and heartbeats (`hello:ai:heartbeat:{replica}`, 5 s, stale at 15 s), `getAITask` ownership; `TestTasksAndLimits` (tasks half, with Valkey).
- [ ] Scheduler (tick 5 s, run-now rows, advisory lock on a dedicated connection, run rows, start delay), agents list and run-now handlers; `TestScheduler` with two schedulers on one database.
- [ ] Prune (hourly, lock, every retention of S-26); `TestPrune`.
- [ ] Metrics of S-25 for the core and the status handler; `TestAIMetrics` (core half). `test/fakellm` with `/v1/chat/completions` scripted per feature and a request log.
- [ ] Mutation-check the privacy check, the budget share and the heartbeat staleness. Full gate.

## Task 3: Proposals (branch ai-agent-proposals)

Files: `internal/ai/proposal/` (`allowlist.go`, `validate.go`, `diff.go`, `fingerprint.go`, `store.go`, `apply.go`, tests incl. `injection_test.go`), `internal/store/ai_proposals.go`, `internal/api/ai_proposals.go` and tests.
Interfaces: produces contract 6 and the proposal routes; consumes contracts 1, 3, 4, 7 and `apispec`.

- [ ] Allowlist with its document check (exists, `write`, no secret in the response): `updateExtension`, `updateDevice`, `createRingGroup`, `updateRingGroup`, `deleteRingGroup`, `putFeatureCodes`, `createOutboundRoute`, `updateOutboundRoute`, `deleteOutboundRoute`, `createInboundRoute`, `updateInboundRoute`, `deleteInboundRoute`, `updateTrunk`, `updatePhone`. Deletes are visually marked and require explicit confirmation. `TestProposalValidation` (allowlist half), `TestProposalDeleteValidation`.
- [ ] Validation: body against the request schema (`jsonschema-go`, unknown properties rejected), path parameters through a replayed `GET`, credential properties refused, route dry run through `internal/routing` compile, at most 8 actions; `before` from the `GET`, `after` = body for `PUT`, merge for `PATCH`, body for `POST`, empty for `DELETE`; `TestProposalValidation`.
- [ ] Delete proposals compute `after` as empty, the diff shows what disappears, and references to the deleted resource are listed; `TestProposalDeleteValidation` fails if a delete diff does not show references.
- [ ] Fingerprint, upsert with refresh, supersede, 7-day dismissed suppression; `TestProposalDedupe`.
- [ ] Get with live `current`, apply (row lock, staleness by revision then per-target compare, ordered replay with the applier's `Cookie`/`Authorization`, `via` `ai-proposal:<id>`, stop on first failure, `409` → stale), dismiss with reason, audit rows; `TestProposalApply`.
- [ ] Injection fixtures for proposals; `TestInjection` (proposal half).
- [ ] Mutation-check the allowlist, the credential-property check, the staleness compare and the header copy. Full gate.

## Task 4: Assistant (branch ai-agent-assistant, after Task 2's `Generate` merges)

Files: `internal/ai/assistant/` (`tools.go`, `session.go`, `task.go`, `output.go`, tests incl. `injection_test.go`), `internal/store/ai_sessions.go`, `internal/api/ai_sessions.go` and tests.
Interfaces: produces the session, message and task routes; consumes contracts 3–6.

- [ ] `assistantTools` from `apispec` (fixed list of S-6), each executed by `replay.Do` under `auth.WithAgent`, redacted, capped at 16 KiB with `truncated`, wrapped in `DataBlock`; `TestAssistantTools`.
- [ ] Sessions and messages (ownership, admin view, 20-message window, title, idle time, one running task per session); `TestAssistantSessions` (store and API).
- [ ] Message task: `Generate` with tools, `MaxSteps` 8, a 16-call stop condition, output `{answer, citations, proposal?}`, citation and proposal validation, stored tool-call summaries; `TestAssistantSessions` (task half) with `aitest` scripts.
- [ ] Injection fixtures through tool results and history; `TestInjection` (assistant half).
- [ ] Mutation-check the read-only tool filter and the call cap. Full gate.

## Task 5: Call quality in CDRs and media relay (branch ai-agent-quality, after Task 1's migration merges)

Files: `migrations/00009_ai_agent.sql` (Task 1 adds the nullable `cdrs` columns `rtp_packets bigint`, `rtp_lost bigint`, `rtp_jitter_ms real`), `internal/sip/media.go` and `internal/sip/takeover.go` (keep the relay's last `RelayStats` snapshot per call), `internal/cdr/cdr.go` (write the three columns), `internal/store/cdr.go` (`RTPPackets *int64`, `RTPLost *int64`, `RTPJitterMs *float64`, JSON `rtpPackets`, `rtpLost`, `rtpJitterMs`), `internal/api/openapi.json` (CDR schema), `web/src/pages/CallDetail.tsx`, tests.
Interfaces: produces the CDR quality columns the `call_quality` detector (Task 6) reads; consumes `internal/media` `RelayStats`/`DirectionStats` (`Packets`, `Lost`, `JitterMs`) unchanged.

- [x] The `relay.Observe` callback in `internal/sip/media.go` (and the takeover re-anchor in `takeover.go`) also stores the latest snapshot on the call; at call end the CDR gets `rtp_packets` = sum of `Packets`, `rtp_lost` = sum of `Lost`, `rtp_jitter_ms` = max of `JitterMs` over both directions; directly-media and unanswered calls keep null. `TestCallQualityCDR` in `internal/cdr`.
- [x] `store.CDR` scans the columns; the CDR schema in `openapi.json` gains `rtpPackets`, `rtpLost`, `rtpJitterMs` (nullable); `TestOpenAPIMatchesRoutes` stays green.
- [x] `CallDetail.tsx` shows loss percent (`rtpLost / (rtpPackets + rtpLost) × 100`) and jitter for anchored calls and "not measured" otherwise; `CallDetail.test.tsx` covers both.

## Task 6: Detectors and findings (branch ai-agent-aiops, after Task 2's scheduler and Task 5's quality merge)

Files: `internal/ai/detect/` (`detect.go` candidate types and the `aiops` agent, `reg_failures.go`, `auth_bruteforce.go`, `trunk_down.go`, `trunk_asr.go`, `node_health.go`, `trunk_capacity.go`, `call_quality.go`, `config_smells.go`, `samples.go`, `findings.go`, `explain.go`, tests, `bench_test.go`), `internal/store/ai_findings.go`, `internal/api/ai_findings.go` and tests.
Interfaces: produces the findings routes and proposals with source `finding:<type>`; consumes contracts 1, 5, 6, `livestate`, `cluster`, `routing`, call quality from CDRs and the store.

- [ ] One detector at a time, test first, a table per detector at, above and below each threshold of S-19 against PostgreSQL and Valkey; `TestDetectors`.
- [ ] Samples (member start times and tombstones, trunk active calls, device registered-today) and their reads; part of `TestDetectors`.
- [ ] Findings upsert, acknowledge, dismiss with 24 h suppression and severity-rise reopen, 30-minute resolve, severity history, health score; `TestFindingsLifecycle`.
- [ ] Explanation: only on set or severity change and once per interval, background budget, validator (known ids, ranks unique, at most one proposal per finding, validated by contract 6), unexplained fallback; `TestFindingsLifecycle` (model half).
- [ ] `call_quality` detector: per trunk (`trunk_name`) and node (`sip_node`), the last 5 CDRs with non-null quality ended in the last 60 min; ≥ 3 with loss ≥ 1 % or `rtp_jitter_ms` ≥ 100 → warning; fewer than 5 raise nothing. `TestDetectors` covers the threshold and one just below it.
- [ ] `TestDetectorLatency` (`HELLO_BENCH=1`) over a generated 1-million-CDR, 10 000-device database; add the indexes it shows are needed to `00009`.
- [ ] Mutation-check each threshold comparison and the suppression window. Full gate.

## Task 7: Console (branch ai-agent-ui)

Files: `web/src/pages/AIAssistant.tsx`, `AIFindings.tsx`, `AIProposals.tsx`, `AIProposalDetail.tsx`, `AIStatus.tsx` and their `*.test.tsx`, `web/src/components/ai/` (`JsonDiff.tsx`, `ProposalCard.tsx`, `FindingCard.tsx`, `TaskStatus.tsx`, `AIOff.tsx`, `PlainText.tsx`), `web/src/pages/Dashboard.tsx` (AI card), `web/src/nav.ts`, `web/src/App.tsx`, `web/src/api/`.
Interfaces: consumes the routes of the spec's Interfaces section (mocked until Tasks 2–6 land).

- [ ] Assistant: sessions, composer (4000 characters), 2 s task polling, plain-text rendering, data sources per answer, inline proposal card; `AIAssistant.test.tsx`.
- [ ] Findings: filters, evidence, explanation or "not explained", acknowledge and dismiss; `AIFindings.test.tsx`.
- [ ] Proposals: inbox by status, detail with the diff (`current` differences marked), apply confirmation listing each operation, dismiss with reason, failure detail naming applied actions; `AIProposals.test.tsx`.
- [ ] Status page and dashboard card, role-aware controls; `AIStatus.test.tsx`. Full gate including `procoder test` and `procoder lint` over `web/`.

## Task 8: kw, docs and end to end (lead, branch ai-agent-contracts, after Tasks 2, 3, 4, 5 and 6 merge)

Files: `deploy/kuvryn-sync/kw/resources.yaml` (hello-control env from Secret `hello-ai`, optional), `deploy/kuvryn-sync/kw/secret-hello-ai.sops.yaml` (fastllm base URL and model, SOPS-encrypted like the other kw secrets), `test/deploy/` (`TestKwAIAgent`, `TestDocsAIAgent`), `test/integration/ai_agent_test.go`, `test/integration/kw_smoke_ai_test.go`, `docs/ai-agent.md`, `README.md`, `docs/ai-access.md` (link), `internal/mcp/` (`TestAIOperationsMCP`).
Interfaces: consumes everything above.

- [ ] Merge the core, proposals, assistant, quality, aiops and ui branches (each by its own PR), resolving conflicts hunk by hunk; full gate.
- [ ] `TestAIAgentEndToEnd` against `test/fakellm`: chat question → tool call replayed as the user → proposal with diff (including a delete proposal marked and confirmed) → apply → config changed with audit; seeded REGISTER flood → explained `auth_bruteforce` finding; dismissed proposal not appliable.
- [ ] `TestNoSecretsInLogs` extended with the API key and a prompt marker from the end-to-end run; `TestAIOperationsMCP`.
- [ ] kw manifest and `TestKwAIAgent`; docs/ai-agent.md with generated tool, allowlist (incl. deletes) and threshold tables and `TestDocsAIAgent`; README link.
- [ ] After merge: pin images, Sync to kw, create Secret `hello-ai` for fastllm, run `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`) from the Arc runner, ask the assistant one question and apply one proposal (incl. a delete) on kw, and record the evidence in the stories.

## Acceptance criteria

See `.procoder/specs/ai-agent.md` — each criterion cites its named test; `TestAIAgentEndToEnd` is the end-to-end proof, and `TestKwSmokeAI` plus one applied proposal on kw close the epic.
