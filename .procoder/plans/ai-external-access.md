# ai-external-access — implementation plan

Status: approved
Spec: .procoder/specs/ai-external-access.md

## Goal

An MCP client pointed at `https://hello.kw.watteel.lab/mcp` discovers Hello's OAuth 2.1 authorization server, gets a user's scoped, revocable consent, and operates Hello through tools generated from a complete, test-checked OpenAPI document — replayed through the same API with the caller's credentials, never seeing a secret — guided by skills shipped in the repository.

## Architecture

Three new packages inside hello-control, around the existing `/api/v1` handler:

- **`internal/apispec`** parses the embedded `openapi.json` once into typed operations (id, method, path, scope, MCP exclusion, parameter and body schemas with `$ref`s inlined, `2xx` schema, secret property paths). The conformance tests, the MCP tool generator and the skills test all read the document through it, so they cannot disagree about it.
- **`internal/oauth`** is the authorization server: metadata, authorize, token, revoke, optional registration, client ID metadata document fetching, the throttle, and the consent operations the console calls through `/api/v1`. Its state lives in PostgreSQL behind `oauth.Store`, implemented in `internal/store`.
- **`internal/mcp`** wraps the official Go SDK's stateless Streamable HTTP handler: tools, resources and prompts derived from `apispec`, executed by replaying a request through `api.Handler` with the caller's `Authorization` header and a context-only replay marker.

`internal/auth` grows scopes, credential kinds and audiences; its middleware takes each route's scope from the route table that `internal/api` now builds. `cmd/hello-control` composes one mux: `/api/v1/` → `api.Handler`, `/mcp` → `mcp.New`, `/oauth/` and `/.well-known/oauth-` → `oauth.Server`. The console adds the consent page and the AI access page; `skills/` is embedded and served for download.

Why in hello-control and not a separate service: the MCP server must replay through the API in-process with the caller's own credentials (the Nexora pattern, `mgmt/internal/api/replay.go`), and the authorization server needs the users, sessions and audit tables hello-control already owns. A separate service would have to re-implement both or call back over HTTP with credentials in flight.

## Constraints

- One new direct dependency: `github.com/modelcontextprotocol/go-sdk` v1.8.x (v1.8.0 verified to carry protocol versions `2026-07-28` and `2025-11-25`, `StreamableHTTPOptions.Stateless`, `CrossOriginProtection`, and the `oauthex` metadata types). `github.com/google/jsonschema-go` comes with it and is used for validation. No OAuth library. No LLM client in phase 1 (later phases use `github.com/azrtydxb/go-ai-sdk`).
- Existing sessions, API tokens, routes and response shapes keep working unchanged; a deployment without `HELLO_PUBLIC_URL` behaves exactly as today.
- No credential, code, verifier, client secret or `x-hello-secret` value in logs, metrics, audit rows or MCP results.
- OAuth 2.1 only: PKCE S256 mandatory, exact redirect URIs (loopback port excepted), single-use 60 s codes, rotating refresh tokens with reuse detection, `iss` on every authorization response, `Cache-Control: no-store` on token responses.
- Each task:
  - leaves `gofmt`, `go vet ./...`, `golangci-lint run ./...` and `go test -race ./...` clean, with the test databases set
  - leaves `procoder check` with 0 blocking findings, and `procoder test` and `procoder lint` green over `web/` where it touches the console
  - gives every security-relevant behaviour a test that fails without it, mutation-checked (snapshot immediately before, restore immediately after, `cmp`)
- The REVIEW.md rubric applies. CI runs on the Arc runners; deployment is Kuvryn Sync; no workloads on the user's Mac.
- Decided 2026-10-08: user roles `viewer`/`operator`/`admin` are built in this phase (spec S-23, Task 4); MCP reads get a log line and metrics, changes their audit rows; DCR is on for kw.

### Shared contracts (fixed; a stream that needs a change asks the lead and never edits another stream's files)

1. **Schema:** `migrations/00008_ai_access.sql`, as the spec's Data section lists it, with `users.role` (existing rows set to `admin`) and `CHECK` lists for `users.role`, `api_tokens.kind`, `oauth_clients.kind` and `oauth_tokens.kind`, and `oauth_tokens.scopes`/`resources` non-null.
2. **Scopes and actors** (`internal/auth/scope.go`):
   - `type Scope string`; `ScopeRead`, `ScopeWrite`, `ScopeAdmin`, `ScopeSecrets`, `ScopeSession`; `AllScopes` (read, write, admin, secrets).
   - `type Scopes []Scope` with `Has(Scope) bool` (admin implies write implies read; `secrets` and `session` only when present), `ParseScopes(string) (Scopes, error)` (space-separated, unknown → error), `String()`.
   - `type Role string`; `RoleViewer`, `RoleOperator`, `RoleAdmin`, ordered, with `AtLeast(Role) bool` and `ParseRole`.
   - `GrantableScopes(Role) Scopes`: viewer → read; operator → read, write; admin → `AllScopes`.
   - `Actor` gains `Role` (the owning user's current role, or the service account's), `Kind` (`KindSession`, `KindLegacyToken`, `KindPersonalToken`, `KindOAuth`, `KindService`), `Scopes`, `ClientID`, `Audience []string`, `ServiceName`. `String()`: `service:<name>` for `KindService`, otherwise as today. Sessions and legacy tokens carry `AllScopes` plus `ScopeSession` for sessions.
   - Prefixes: `PrefixPersonal = "hello_pat_"`, `PrefixAccess = "hello_at_"`, `PrefixRefresh = "hello_rt_"`, `PrefixClientSecret = "hello_cs_"`; `NewPrefixed(prefix) (plain string, hash []byte)` over `NewToken`.
   - Replay marker: `WithReplay(ctx, Replay{ClientID string})`, `ReplayFrom(ctx) (Replay, bool)`; the middleware accepts an MCP-audience access token on `/api/v1` only when `ReplayFrom` is set.
3. **Middleware** (`internal/auth/middleware.go`): `Middleware(l Lookup, o Options, log)` with `Options{Resource string; MetadataURL string; Cookies bool}`, and `Require(r Role, s Scope) func(http.Handler) http.Handler`, which checks the role (`403` `forbidden_role`) and then the scope (the spec's `403` challenge). `Lookup.TokenActor` resolves every bearer kind (prefix decides the table; unprefixed is legacy).
4. **Route table** (`internal/api/routes.go`): `type route struct{ Method, Pattern string; Scope auth.Scope; Role auth.Role; Public bool; H http.HandlerFunc }`, `func (s *server) routes() []route`, and exported `RouteTable() []RouteInfo{Method, Pattern string; Scope auth.Scope; Role auth.Role; Public bool}` for tests outside the package. `Handler` registers from it. `OpenAPI() []byte` exports the embedded document.
5. **apispec** (`internal/apispec/apispec.go`): `Load([]byte) (*Spec, error)`; `Spec.Operations() []Operation`; `Operation{ID, Method, Path, Summary, Description string; Scope auth.Scope; Role auth.Role; Exclude string; Params []Param; Body *Body; Output map[string]any; Secrets []string}`; `Param{Name, In, Description string; Required bool; Schema map[string]any}`; `Body{ContentTypes []string; JSON map[string]any}`; `Secrets` are JSON-pointer-like paths (`/secret`, `/items/*/url`).
6. **OAuth** (`internal/oauth/oauth.go`, `store.go`): `New(Options) (*Server, error)` with `Options{Store Store; PublicURL string; AccessTTL, RefreshIdle, RefreshMax time.Duration; DCR, CIMDAllowPrivate bool; Limiter Limiter; Client *http.Client; Metrics *Metrics; Log *slog.Logger}`; `(*Server).Handler() http.Handler` (well-knowns and `/oauth/*`); `(*Server).Request(ctx, id) (ConsentView, error)`, `Approve(ctx, auth.Actor, id string, Scopes) (redirect string, error)`, `Deny(ctx, auth.Actor, id string) (redirect string, error)`; `MetadataURL(resource string) string`; `Resources() (api, mcp string)`. `Store` lists the client, request, grant, token and secret operations Task 3 needs (written by the lead with Task 3's first commit and fixed thereafter).
7. **MCP** (`internal/mcp/mcp.go`): `New(Options) (http.Handler, error)` with `Options{API http.Handler; Spec *apispec.Spec; PublicURL string; AllowedOrigins []string; Lookup auth.Lookup; MetadataURL string; Metrics *Metrics; Log *slog.Logger}`.
8. **Config** (`internal/config`): `Control.AI{PublicURL string; AccessTTL, RefreshIdle, RefreshMax time.Duration; DCR, CIMDAllowPrivate bool; MCPAllowedOrigins []string}` from the spec's env keys.

Recorded at Task 1 (deviations and decisions the streams build on):

- Contract 4: the exported table is `api.Routes() []RouteInfo`, not `RouteTable()`, because `api.RouteTable` is already the call-routing interface. Tests that the plan names as iterating `RouteTable()` iterate `Routes()`.
- Contract 4: the rows of every new route in the spec's Interfaces section are in `routes()` already, with their scope and role, answering `501` `not_implemented` through `s.pending`; a stream replaces `s.pending` in its rows and edits nothing else in `routes.go`. Choices the spec left open: `GET /oauth/requests/{id}` is `read`; `DELETE /oauth/grants/{id}` is `session` (revocation from the console; the handler applies the admin rule for other users' grants); `GET /tokens` and `GET /prov/redirect` are `admin` (credentials); `POST /auth/logout` is `read`; skills and `ai/settings` are `read`; users and service accounts are `admin`. `TestRoutesTable` holds the scope-to-role rule and the `secrets` list.
- Contract 3: `Require` is a pass-through until Task 3, and `Middleware` fills `Kind` and `Scopes` for sessions (`AllScopes` + `session`) and unprefixed tokens (`legacy_token`, `AllScopes`) when the lookup leaves them empty; `Options.Cookies` is honoured, `Resource` and `MetadataURL` are Task 3's.
- Contract 1: `oauth_clients` carries `role` (service accounts, with a `CHECK` that a `service` row has a role and scopes); `oauth_requests`, `oauth_grants` and `oauth_tokens` reference `oauth_clients` (cascade), `oauth_tokens.grant_id` references `oauth_grants` (cascade); `oauth_clients.created_by` is set null when the user is deleted.
- Contracts 5–7: `apispec.Load` checks the document's shape and `Operations()` is empty until Task 2; `oauth.Server` computes `Resources`/`MetadataURL` and its endpoints answer `404` and consent operations `ErrNotImplemented` until Task 3; `oauth.Store` is empty until Task 3's first commit; `mcp.New` serves an empty stateless SDK server until Task 5. `oauth.Limiter` is `Allow(ctx, key, limit, window) (bool, error)`; `oauth.Metrics` and `mcp.Metrics` are empty structs until their tasks.
- Contract 8: `HELLO_OAUTH_DCR` and `HELLO_OAUTH_CIMD_ALLOW_PRIVATE` accept only `true`/`false`; `HELLO_OAUTH_REFRESH_TTL` must not exceed `HELLO_OAUTH_REFRESH_MAX`; `AI.Enabled()` is `PublicURL != ""`.

## Task 1: Shared contracts (lead, branch ai-contracts)

Files: `migrations/00008_ai_access.sql`, `internal/auth/scope.go`, `internal/auth/middleware.go`, `internal/api/routes.go`, `internal/api/api.go`, `internal/apispec/apispec.go` (types and `Load` only), `internal/oauth/oauth.go` and `store.go` (types and signatures), `internal/mcp/mcp.go` (signature), `internal/config/config.go`, `go.mod`, `go.sum`, this plan.
Interfaces: everything listed in Shared contracts.

- [x] Write the migration; `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/` → applies and rolls back (`TestMigrateAIAccessRollback`).
- [x] Move every `public(...)`/`private(...)` call in `Handler` into `routes()` with a scope per the spec's S-5 list and a minimum role per S-23 (read → viewer, write → operator, admin and secrets → admin, session → viewer); the middleware enforces neither yet, so no behaviour change: `go test ./internal/api/` → all existing tests pass unchanged.
- [x] Add `scope.go`, the `Actor` fields, prefixes and the replay marker; `go test ./internal/auth/` → `TestScopesHas` (hierarchy, `secrets` orthogonal, `session` session-only) passes.
- [x] `go get github.com/modelcontextprotocol/go-sdk@v1.8.0`; `go build ./...` → ok.
- [x] Config fields with defaults and validation (`HELLO_PUBLIC_URL` https scheme-and-host, `http://localhost` allowed); `go test ./internal/config/` → `TestLoadAI*` pass.
- [x] Commit to `ai-contracts` and merge it to main (PR); the `ai-openapi`, `ai-oauth`, `ai-roles`, `ai-mcp` and `ai-ui-skills` branches start from that main, each in its own worktree, when their streams start.

## Task 2: OpenAPI completeness (branch ai-openapi)

Files: `internal/api/openapi.json`, `internal/apispec/` (`Load` implementation, `$ref` inlining, secret paths, tests), `internal/api/conformance_test.go` (the validator and `TestMain`), `internal/api/openapi_test.go` (`TestRoutesMatchOpenAPI`, `TestOpenAPIForTools`), the existing `internal/api/*_test.go` files only to add cases that reach documented statuses.
Interfaces: produces the completed document (every operation with `x-hello-scope`, `x-hello-mcp`, descriptions, `x-hello-secret`) and `apispec`; consumes contract 4.

- [x] `apispec.Load`: operations, parameters (path and query, path required), JSON body schemas and `2xx` schemas with `$ref` inlined to depth 8, `x-hello-*` fields, secret paths; table test over a small fixture document.
- [x] `TestRoutesMatchOpenAPI`: `RouteTable()` versus `apispec` operations, both directions, scope and role equality.
- [x] Add `x-hello-scope`, `x-hello-role` and `x-hello-mcp` to all 103 operations (exclusions and reasons per spec S-14), descriptions for the 61 without one and for every parameter and top-level body property, `x-hello-secret` on the show-once properties; `TestOpenAPIForTools` passes.
- [x] The validator: wrap `Handler` in tests (installed in `TestMain`), validate request bodies and responses with `jsonschema-go`, record observed `(operation, status)` pairs, detect handlers that read a body without a documented `requestBody`; at exit, fail on violations and on documented statuses never observed outside the exemptions (commented list in `conformance_test.go`).
- [x] Fix the spec S-3 gaps: preview `422`, the seven `502` entries, the voicemail PUT `oneOf` body and the heard body; add the test cases that produce each, and every reachable `409`/`400`; remove documented statuses no handler can send. `TestOpenAPIConformance` passes.
- [x] Mutation-check: drop one route from the table, one operation from the document, one documented status, one `requestBody` — each fails the suite. Run the full gate.

Recorded at Task 2:

- Contract 5 grows two read-only methods, `Spec.Responses(id)` (status → inlined JSON schema, nil without a JSON body) and `Spec.RequestContent(id)` (content type → inlined schema), which the conformance validator needs; `Operation` is unchanged. A `$ref` past depth 8 keeps only its sibling keywords.
- The validator is installed through a package variable `wrapForTest` in `api.go` (identity outside tests) wrapping `Handler`'s result; it matches requests to operations itself. A request body is checked against the document only when the API accepted it (`2xx`); a rejected one is the suite probing validation. A request without `Content-Type` is read as JSON (the handlers' default). `TestOpenAPIConformance` runs from `TestMain` after the suite; the observed-status check runs only on a full run (`HELLO_TEST_DATABASE_URL`, `HELLO_TEST_MINIO_ENDPOINT` and `HELLO_TEST_VALKEY_ADDR` set, no `-run`/`-skip`/`-short`). No exemptions were needed.
- `TestRoutesMatchOpenAPI` skips route rows whose handler is still `s.pending`; each stream's rows become required in the document the moment it replaces `s.pending`.
- `importPhones` is excluded from MCP (`text/csv` body; tools send JSON), beyond S-14's list. Public operations carry no `x-hello-scope`/`x-hello-role`, as their route rows.
- Document changes found by the validator: `403` on `login` (cross-origin refusal), `409` on `deleteDevice`, the `importPhones` `400` as `oneOf` error or result, `upstream` and `render_error` in `Error.code`; `listPresence` answered `items: null` with no devices and now answers `[]`.

## Task 3: Authorization server and scoped credentials (branch ai-oauth)

Files: `internal/oauth/` (`metadata.go`, `authorize.go`, `cimd.go`, `token.go`, `revoke.go`, `register.go`, `throttle.go`, `metrics.go`, tests), `internal/auth/` (middleware scope and audience checks, tests), `internal/store/` (contract 6 `Store`, token lookups by prefix, `api_tokens` scopes/expiry/revocation, service accounts and secrets, grants, `audit_events.via`, the daily prune), `internal/api/` (handlers for the consent, grants, service-account, token and `ai/settings` routes and their OpenAPI entries; the route table rows are Task 1's), `cmd/hello-control/main.go` (the composed mux, wiring, prune lease).
Interfaces: produces contract 6 and the `Lookup` behaviour of contract 3; consumes contracts 1–4.

- [x] Store: every credential, client, grant and consent change in one transaction with its audit row; `TokenActor` dispatches on prefix and returns scopes, kind, client and audience; expired, revoked, spent and disabled credentials return `ErrNoCredentials`. `TestTokenLifecycle`.
- [x] Middleware: `Require(role, scope)` from the route table (role read per request from the owning user or service account), audience check (`/api/v1` resource, or MCP resource inside a replay), `WWW-Authenticate` on `401` and `403`, `session` scope only for cookies. `TestScopeEnforcement` iterates `RouteTable()` with a token one scope short and one exactly sufficient.
- [x] Metadata documents and the `404`s without `HELLO_PUBLIC_URL`; `TestOAuthMetadata` (decodes with `oauthex.AuthServerMeta` and `oauthex.ProtectedResourceMetadata`).
- [x] CIMD fetcher: `https` only, no redirects, 5 s, 64 KiB, dial-time refusal of private, loopback and link-local addresses (unless allowed), `client_id` equality, cache by `Cache-Control`/`Expires` capped at 24 h; table test with `httptest` servers and a custom dialer.
- [x] Authorize and consent: request validation order (client and redirect URI first, error page before that point), PKCE S256, `resource` normalisation, stored request, redirect to `/oauth/consent`; `Request`, `Approve` (narrowing only, `GrantableScopes`, code issue), `Deny`; `TestAuthorizeAndConsent`.
- [x] Token endpoint: code exchange with reuse revocation, refresh rotation with reuse revocation and idle/absolute limits, client credentials, RFC 6749 errors, `no-store`; `TestTokenEndpoint`.
- [x] Revocation and grants: `/oauth/revoke`, grant list and delete with the `admin` rule; `TestGrantsAndRevocation`.
- [x] Service accounts: CRUD with a role, scopes bounded by `GrantableScopes(role)`, at most two live secrets, disable; `TestServiceAccounts`.
- [x] DCR behind `HELLO_OAUTH_DCR`, public clients only, cleanup in the daily prune; `TestDynamicRegistration`. Throttle in Valkey with in-memory fallback; `TestOAuthThrottle`.
- [x] Metrics `hello_oauth_*`. Mutation-check PKCE verification, code reuse revocation, refresh reuse revocation, the audience check, the CIMD private-address refusal and the `session` scope rule. Run the full gate.

Recorded at Task 3:

- Contract 6: `oauth.Store` is written (clients, requests, `RedeemCode`/`Refresh`/`ClientCredentials` taking an `issue` callback inside their transaction, grants, service accounts and secrets, `PruneOAuth`); its errors are `oauth.ErrNotFound`, `ErrInvalidGrant`, `ErrReused`, `ErrTooManySecrets`, `ErrConflict`. `ErrNotImplemented` is gone. The server also exposes `Grants`, `RevokeGrant`, the service-account operations, `Issuer`, `DCR` and `Prune`, which `api.AIAccess` (nil without `HELLO_PUBLIC_URL`) consumes.
- Contract 3: `TokenActor` gets only the hash, so it probes `api_tokens` and `oauth_tokens` in one statement by their unique hash indexes rather than dispatching on the prefix. The middleware drops a stored `session` scope from any token. Inside a replay the MCP resource is `Resource` with `/api/v1` replaced by `/mcp`, and the replay's client must be the token's.
- `oauth_tokens` records no secret, so revoking a service-account secret (or disabling, re-roling or re-scoping the account) revokes all of the account's live access tokens; clients re-run client credentials.
- The schema has no request-to-grant link, so a reused code revokes every grant the user gave that client since the request (a superset of what the code issued).
- Audit `via` comes from the request context's actor, so every store mutation made with an OAuth credential carries it without signature changes. `store.CreateUser` makes the first user `admin` and later ones `viewer`, and `CreateFirstUser` (bootstrap) makes `admin`; Task 4 adds the CLI `--role`.
- `store.NewToken` with nil `Scopes` stores a legacy token (tests and tools); `POST /tokens` always creates `hello_pat_` personal tokens, never with a scope the caller's credential or role lacks.
- Loopback redirect URIs include `localhost`, besides `127.0.0.1` and `[::1]`, because Claude Code registers `http://localhost:<port>/callback`.
- Tests: `TestTokenLifecycle` (auth, store, api), `TestScopeEnforcement` (api), `TestOAuthMetadata`, `TestAuthorizeAndConsent`, `TestCIMDFetch`, `TestTokenEndpoint`, `TestGrantsAndRevocation`, `TestServiceAccounts`, `TestDynamicRegistration`, `TestOAuthThrottle`, `TestOAuthMetrics`, `TestNoSecretsInOAuthLogs` (oauth, on an in-memory store), `TestServiceAccountsAPI`, `TestGrantsAPI`, `TestServiceAccountStore` (database), `TestComposeWithoutPublicURL` (cmd). The PKCE check, code- and refresh-reuse revocation, the audience and replay-client checks, the CIMD private-address refusal, the session-scope strip, session-only approval, `Require`'s role and scope checks, the grantable bound, refresh narrowing, the throttle and exact redirect matching were each mutated and caught.

## Task 4: User roles (branch ai-roles, after Task 3's middleware)

Files: `internal/store/` (user role reads and `SetUserRole` with the last-admin check and audit row), `internal/api/users.go` and its test (`GET /api/v1/users`, `PATCH /api/v1/users/{id}`, `role` on `auth/me`, their OpenAPI entries), `cmd/hello-control` (`user add --role`), `web/src/pages/Users.tsx` and `Users.test.tsx`, `web/src/nav.ts` and `web/src/App.tsx` (role-aware navigation from `auth/me`).
Interfaces: consumes contracts 1–4 and Task 3's middleware; produces the users routes the console calls.

- [ ] Store and API: list users with roles, change a role (admin; `409` `last_admin`; audited), `role` on `auth/me`; CLI `--role` (default `viewer`, first user `admin`). `TestUserRoles`.
- [ ] `TestRoleEnforcement` iterates `RouteTable()` with a viewer, an operator and an admin through a session, a personal token, an OAuth token and a service account; one case demotes a user and expects the next request refused.
- [ ] Console: Users page (admin) with a role editor; navigation and write actions hidden below the needed role. `Users.test.tsx`.
- [ ] Mutation-check the role comparison, the per-request role read and the last-admin guard. Run the full gate.

## Task 5: MCP server (branch ai-mcp)

Files: `internal/mcp/` (`server.go` transport and auth, `tools.go` generation and filtering, `replay.go`, `redact.go`, `resources.go`, `prompts.go`, `metrics.go`, tests, `bench_test.go`).
Interfaces: produces contract 7; consumes contracts 2, 3 and 5 and `api.Handler` (a fake handler in unit tests, the real one with fakes in `TestToolReplay`).

- [x] Transport: SDK `mcp.NewServer` per request with the caller's tool set, `mcp.NewStreamableHTTPHandler` with `Stateless: true`, `JSONResponse: true` and a `CrossOriginProtection` trusting the public origin and `HELLO_MCP_ALLOWED_ORIGINS`; bearer-only authentication through `auth.Lookup` with the MCP audience; 1 MiB body cap; `TestMCPTransport` initializes with both protocol versions through the SDK client.
- [x] Tools: one per non-excluded operation, name, title, description, input schema (parameters plus `body`), output schema for object `2xx` bodies, annotations by method; `tools/list` filtered by `Scopes.Has`; a call beyond scope answers HTTP `403` with the challenge; `TestToolsFromOpenAPI` iterates every operation.
- [x] Replay: build the request from the arguments (escaped path parameters, query, JSON body), copy only `Authorization`, set `auth.WithReplay`, refuse nested replay, 30 s timeout, map results (`structuredContent`, `isError` with code, message, fields, 64 KiB text cap); `TestToolReplay` runs against `api.Handler` with the store fakes of `internal/api`.
- [x] Redaction: walk the decoded result by `Operation.Secrets` and replace values; a test per operation that has secrets, device create included.
- [x] Resources and prompts of spec S-16, read through the same replay; `TestResourcesAndPrompts`.
- [x] Tool-call log line (tool, actor, client, status, duration) and `hello_mcp_*` metrics; `TestOAuthMCPMetrics` (MCP half). `TestToolCallOverhead` (`HELLO_BENCH=1`).
- [x] Mutation-check the scope filter, the step-up `403`, the replay-marker refusal for external requests and the redaction walk. Run the full gate.

## Task 6: Console and skills (branch ai-ui-skills)

Files: `web/src/pages/Consent.tsx` and `Consent.test.tsx`, `web/src/pages/AIAccess.tsx` and `AIAccess.test.tsx`, `web/src/pages/Login.tsx` (return-to), `web/src/pages/System.tsx` (tokens move out, a link remains), `web/src/nav.ts`, `web/src/App.tsx`, `web/src/api/` (typed calls), `skills/hello-setup/`, `skills/hello-routing/`, `skills/hello-troubleshoot/` (each `SKILL.md` and `references/`), `skills/skills.go` and `skills/skills_test.go`, `internal/api/skills.go` and its test (handlers and OpenAPI entries; route rows are Task 1's).
Interfaces: consumes the consent, grants, service-account, token, skills and `ai/settings` routes of the spec's Interfaces section (against mocked responses until Task 3 lands) and the tool names `apispec` derives.

- [ ] Consent page: client name and host, resources, scopes with plain-language lines, `secrets` unchecked with its warning, non-grantable scopes disabled, approve and deny then `window.location` to the returned URL; login return-to; `Consent.test.tsx`.
- [ ] AI access page: MCP URL and metadata from `GET /api/v1/ai/settings`, scope explanations, connected apps with revoke, service accounts and secrets (admin, shown once), personal tokens with scopes and expiry, skills list with downloads; `AIAccess.test.tsx`.
- [ ] Skills: write the three skills against the tool names in the completed document (from `ai-openapi`; until it merges, against `operationId`s, which do not change), each with connecting instructions, workflows, and `references/` (tool tables, worked examples, failure meanings); `TestSkills`.
- [ ] Download routes: list and zip from the embedded FS; `TestSkillsDownload`.
- [ ] Run the full gate including `procoder test` and `procoder lint` over `web/`.

## Task 7: Lab, kw, docs and end to end (lead, branch ai-contracts)

Files: `deploy/docker-compose/compose.yaml` (the public URL, the UI proxy locations, a lab certificate), `web/nginx/default.conf.template` (`/mcp`, `/oauth/`, `/.well-known/oauth-` locations), `deploy/kuvryn-sync/kw/resources.yaml` (`hello-tls` Certificate from `cluster-ca`, TLS on the `hello` Ingress, `HELLO_PUBLIC_URL`, `HELLO_OAUTH_DCR=true`), `test/deploy/` (`TestKwAIAccess`, `TestDocsAIAccess`), `test/integration/mcp_e2e_test.go` (`TestMCPEndToEnd`), `test/integration` (`TestNoSecretsInLogs` extension), `docs/ai-access.md`, `README.md`.
Interfaces: consumes everything above.

- [ ] Merge the openapi, oauth, roles, mcp and ui-skills branches (each by its own PR), resolving conflicts hunk by hunk; run the full gate.
- [ ] `TestMCPEndToEnd`: hello-control with the lab database, a test-served client ID metadata document, the SDK client's authorization-code handler driving `/oauth/authorize`, consent approved through the API with a session, tool calls, a resource read, refresh, revoke, refusal — each step of spec S-22.
- [ ] Extend `TestNoSecretsInLogs` with every credential kind and a withheld value from the end-to-end run.
- [ ] kw manifest, nginx template and `TestKwAIAccess`.
- [ ] docs/ai-access.md per spec S-21, its tool and resource tables generated by `go run ./internal/mcp/cmd/doctable` and checked by `TestDocsAIAccess`; README link.
- [ ] After merge: pin images, Sync to kw, and from the LAN connect Claude Code to `https://hello.kw.watteel.lab/mcp` (with `cluster-ca` trusted), complete consent, list tools, create and delete a test extension, and record the evidence in the stories.

## Acceptance criteria

See `.procoder/specs/ai-external-access.md` — each criterion cites its named test; `TestMCPEndToEnd` is the end-to-end proof, and the live connection from Claude Code on kw closes the epic.
