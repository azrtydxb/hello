# ai-agent — implementation plan

Status: draft (waits for the spec's open questions)
Spec: .procoder/specs/ai-agent.md

## Goal

With `HELLO_AI_BASE_URL` and `HELLO_AI_MODEL` pointing at kw's fastllm, an operator asks the console assistant a question about the PBX and gets an answer grounded in read-only tool calls replayed as them, plus a validated proposal shown as a diff that they apply through Hello's own API; deterministic detectors raise PBX findings (REGISTER floods, trunk failures and answer-seizure drops, node flaps, capacity, config smells) that the model explains and ranks — and nothing in Hello changes without a human apply.

## Architecture

All inside hello-control, beside the API:

- **`internal/replay`** is phase 1's in-process replay (`internal/mcp/replay.go`) moved out so MCP, the assistant and proposal apply share one function: `Do(ctx, api http.Handler, Request) Result`, copying only the headers the caller passes, refusing nested replays, with phase 1's timeout, body cap and panic recovery; `Redact(v, op)` withholds `x-hello-secret` values. MCP keeps its behaviour and tests.
- **`internal/ai`** is the bounded service (provider over go-ai-sdk, privacy dialer, limits, usage and budget, `Generate` with validator retry, `DataBlock`), tasks with Valkey heartbeats, the scheduler with advisory locks, prune and metrics.
- **`internal/ai/proposal`**: allowlist, validation against `apispec`, before/after, fingerprint, store, apply and dismiss.
- **`internal/ai/assistant`**: sessions, messages, the read-tool adapter (`apispec` operation → go-ai-sdk tool, executed by `replay.Do` under an agent identity), and the message task.
- **`internal/ai/detect`**: one file per detector, the `aiops` agent, samples, findings lifecycle and the explanation call.
- **`internal/api`** gains the `/api/v1/ai/…` handlers (thin: auth, decode, call the packages); `cmd/hello-control` wires the service when AI is enabled. The console gets four pages.

Why tools for reads and structured output for the answer, and why in hello-control: spec S-6 and S-7; the replay and the users, audit and config tables are hello-control's, and a separate service would need credentials in flight.

## Constraints

- One new direct dependency: `github.com/azrtydxb/go-ai-sdk` `v0.6.0`. Models only in `internal/ai/provider.go`; unit tests use `ai/aitest` mock models (helpers in `internal/ai/aifake`); integration tests a scripted OpenAI-compatible fixture in `test/fakellm` (per-feature response queues, records requests); the kw smoke test the real fastllm.
- No hot-path change: hello-sip, provisioning and config writes are untouched except the replay move.
- With AI unconfigured, nothing starts and the new operations answer `503` `ai_disabled`.
- Each task leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...` (test databases set), `procoder check` 0 blocking, and `procoder test`/`procoder lint` over `web/` (where touched) green; safety branches are mutation-checked (snapshot immediately before, restore immediately after, `cmp`). REVIEW.md applies. CI on the Arc runners; kw via Kuvryn Sync; nothing on the user's Mac.
- Open questions 1–3 of the spec gate Task 5's quality detector, Task 2's masking and Task 3's allowlist; the rest does not depend on them.

### Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Schema:** `migrations/00009_ai_agent.sql` with its down migration, as the spec's Data section lists it, `CHECK` lists on every status, severity, outcome and role column.
2. **Config** (`internal/config`): `Control.AIAgent{Provider, BaseURL, Model, APIKey string; AllowPublic bool; StructuredOutput string; ValidationAttempts, MaxSteps, MaxConcurrency, RequestsPerMinute int; Timeout time.Duration; DailyTokenBudget int64; BackgroundBudgetPercent int; AgentStartDelay, AIOpsInterval, ExplainMinInterval time.Duration}` from the spec's env keys; `Enabled() (bool, reason string)`.
3. **Replay** (`internal/replay`): `type Request struct{ Method, Path string; Query url.Values; Body []byte; Header http.Header }`, `type Result struct{ Status int; Header http.Header; Body []byte; Fail string }`, `Do(ctx, h http.Handler, r Request) (Result, error)`, `Redact(v any, secretPaths []string)`; `ErrNested`.
4. **Agent identity** (`internal/auth`): `type Agent struct{ UserID int64; TaskID string }`, `WithAgent(ctx, Agent)`, `AgentFrom(ctx) (Agent, bool)`; the middleware, for a request without credentials whose context holds an agent, loads the user (`Lookup.UserActor(ctx, id)`), sets `Kind = KindAgent`, `Scopes = {read}`, current `Role`, and `Via = "ai-assistant"`.
5. **AI service** (`internal/ai`): `New(cfg, store Store, vk valkey.Client, reg prometheus.Registerer, log) (*Service, error)`; `Generate`, generic over the output type, taking `(ctx, svc, call)` and returning the decoded value, `Usage` and an error, where the call is `Call{Feature string; Background bool; System string; Data any; Prompt string; Tools []aisdk.Tool; MaxSteps int; Validate func(context.Context, T) error}` (typed by the output); `DataBlock(v any) string`; error codes as `*ai.Error{Code string}`; `Tasks.Start(ctx, kind, userID, sessionID, fn) (taskID, error)`; `type Agent interface{ Name() string; Interval() time.Duration; Run(ctx) (Outcome, error) }`, `Scheduler.Register(Agent)`.
6. **Proposal** (`internal/ai/proposal`): `Action{OperationID string; PathParams map[string]string; Body json.RawMessage; Before, After json.RawMessage}`, `Draft{Source, Title, Rationale string; SessionID, FindingID *uuid; Actions []Action}`, `(*Validator).Validate(ctx, ident Identity, d *Draft) error` (fills `Before`/`After`), `Store.Upsert(ctx, Draft) (id, error)` (dedupe, supersede), `Apply(ctx, id, userHeader http.Header) (Proposal, error)`, `Dismiss(ctx, id, userID, reason, text)`.
7. **Routes** (`internal/api/routes.go`): every operation of the spec's Interfaces section in `routes()` with its scope and role answering `501` through `s.pending` until its stream lands, and in `openapi.json` with descriptions, `x-hello-*` and schemas, so phase 1's sync and conformance tests hold from Task 1 on.

## Task 1: Shared contracts (lead, branch ai-agent-contracts)

Files: `migrations/00009_ai_agent.sql`, `internal/config/`, `internal/replay/` (moved), `internal/mcp/replay.go` and `redact.go` (now thin callers), `internal/auth/middleware.go`, `internal/auth/scope.go`, `internal/api/routes.go`, `internal/api/openapi.json`, `internal/ai/ai.go` (types and signatures), `internal/ai/proposal/proposal.go` (types), `go.mod`.
Interfaces: produces contracts 1–7.

- [ ] Migration up and down; `go test -run Migrate ./test/integration/` → `TestMigrateAIAgentRollback` passes.
- [ ] Move replay to `internal/replay`, MCP calls it; `go test -race ./internal/mcp/ ./internal/replay/` → phase 1's `TestToolReplay` and `TestResourcesAndPrompts` unchanged and green.
- [ ] Agent identity in `internal/auth`; `TestAgentIdentity` (the `internal/auth` half: no header sets it, scope is `read` only, deleted user `401`, demotion applies, `via` set).
- [ ] Config keys with defaults and validation (provider enum, structured-output enum, percent 1–100, positive bounds); `TestLoadAIAgent*`.
- [ ] Route rows and OpenAPI operations for every new endpoint (`501` pending); `TestRoutesMatchOpenAPI`, `TestOpenAPIForTools`, `TestRoleEnforcement` green.
- [ ] `go get github.com/azrtydxb/go-ai-sdk@v0.6.0`; `go build ./...`. Commit, PR, merge; streams branch from that main in their own worktrees.

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

- [ ] Allowlist with its document check (exists, `write`, no secret in the response); `TestProposalValidation` (allowlist half).
- [ ] Validation: body against the request schema (`jsonschema-go`, unknown properties rejected), path parameters through a replayed `GET`, credential properties refused, route dry run through `internal/routing` compile, at most 8 actions; `before` from the `GET`, `after` = body for `PUT`, merge for `PATCH`, body for `POST`; `TestProposalValidation`.
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

## Task 5: Detectors and findings (branch ai-agent-aiops, after Task 2's scheduler merges)

Files: `internal/ai/detect/` (`detect.go` candidate types and the `aiops` agent, `reg_failures.go`, `auth_bruteforce.go`, `trunk_down.go`, `trunk_asr.go`, `node_health.go`, `trunk_capacity.go`, `config_smells.go`, `samples.go`, `findings.go`, `explain.go`, tests, `bench_test.go`), `internal/store/ai_findings.go`, `internal/api/ai_findings.go` and tests.
Interfaces: produces the findings routes and proposals with source `finding:<type>`; consumes contracts 1, 5, 6, `livestate`, `cluster`, `routing` and the store.

- [ ] One detector at a time, test first, a table per detector at, above and below each threshold of S-19 against PostgreSQL and Valkey; `TestDetectors`.
- [ ] Samples (member start times and tombstones, trunk active calls, device registered-today) and their reads; part of `TestDetectors`.
- [ ] Findings upsert, acknowledge, dismiss with 24 h suppression and severity-rise reopen, 30-minute resolve, severity history, health score; `TestFindingsLifecycle`.
- [ ] Explanation: only on set or severity change and once per interval, background budget, validator (known ids, ranks unique, at most one proposal per finding, validated by contract 6), unexplained fallback; `TestFindingsLifecycle` (model half).
- [ ] `TestDetectorLatency` (`HELLO_BENCH=1`) over a generated 1-million-CDR, 10 000-device database; add the indexes it shows are needed to `00009`.
- [ ] Call quality per the answer to the spec's open question 1 (a CDR column and hello-sip change, a Prometheus reader, or nothing).
- [ ] Mutation-check each threshold comparison and the suppression window. Full gate.

## Task 6: Console (branch ai-agent-ui)

Files: `web/src/pages/AIAssistant.tsx`, `AIFindings.tsx`, `AIProposals.tsx`, `AIProposalDetail.tsx`, `AIStatus.tsx` and their `*.test.tsx`, `web/src/components/ai/` (`JsonDiff.tsx`, `ProposalCard.tsx`, `FindingCard.tsx`, `TaskStatus.tsx`, `AIOff.tsx`, `PlainText.tsx`), `web/src/pages/Dashboard.tsx` (AI card), `web/src/nav.ts`, `web/src/App.tsx`, `web/src/api/`.
Interfaces: consumes the routes of the spec's Interfaces section (mocked until Tasks 2–5 land).

- [ ] Assistant: sessions, composer (4000 characters), 2 s task polling, plain-text rendering, data sources per answer, inline proposal card; `AIAssistant.test.tsx`.
- [ ] Findings: filters, evidence, explanation or "not explained", acknowledge and dismiss; `AIFindings.test.tsx`.
- [ ] Proposals: inbox by status, detail with the diff (`current` differences marked), apply confirmation listing each operation, dismiss with reason, failure detail naming applied actions; `AIProposals.test.tsx`.
- [ ] Status page and dashboard card, role-aware controls; `AIStatus.test.tsx`. Full gate including `procoder test` and `procoder lint` over `web/`.

## Task 7: kw, docs and end to end (lead, branch ai-agent-contracts)

Files: `deploy/kuvryn-sync/kw/resources.yaml` (hello-control env from Secret `hello-ai`, optional), `deploy/kuvryn-sync/kw/secret-hello-ai.sops.yaml` (fastllm base URL and model, SOPS-encrypted like the other kw secrets), `test/deploy/` (`TestKwAIAgent`, `TestDocsAIAgent`), `test/integration/ai_agent_test.go`, `test/integration/kw_smoke_ai_test.go`, `docs/ai-agent.md`, `README.md`, `docs/ai-access.md` (link), `internal/mcp/` (`TestAIOperationsMCP`).
Interfaces: consumes everything above.

- [ ] Merge the core, proposals, assistant, aiops and ui branches (each by its own PR), resolving conflicts hunk by hunk; full gate.
- [ ] `TestAIAgentEndToEnd` against `test/fakellm`: chat question → tool call replayed as the user → proposal with diff → apply → config changed with audit; seeded REGISTER flood → explained `auth_bruteforce` finding; dismissed proposal not appliable.
- [ ] `TestNoSecretsInLogs` extended with the API key and a prompt marker from the end-to-end run; `TestAIOperationsMCP`.
- [ ] kw manifest and `TestKwAIAgent`; docs/ai-agent.md with generated tool, allowlist and threshold tables and `TestDocsAIAgent`; README link.
- [ ] After merge: pin images, Sync to kw, create Secret `hello-ai` for fastllm, run `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`) from the Arc runner, ask the assistant one question and apply one proposal on kw, and record the evidence in the stories.

## Acceptance criteria

See `.procoder/specs/ai-agent.md` — each criterion cites its named test; `TestAIAgentEndToEnd` is the end-to-end proof, and `TestKwSmokeAI` plus one applied proposal on kw close the epic.
