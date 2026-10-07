# ai-external-access

Status: draft

Source: `.procoder/backlog/milestones/ai-external-access.md` and its epic `ai-external-access`. Phase 1 of the AI integration decided 2026-10-07 (`.procoder/ask/decisions.md`, "AI integration (2026-10-07)"):

- **order:** phase 1 is OpenAPI completeness, an MCP server and agent skills; phase 2 (the in-product suggest-only agent) and phase 3 (voice agents) get their own specs
- **external agent auth:** full OAuth 2.1 now; Hello is the authorization server for MCP clients, with protected resource metadata, PKCE and client ID metadata documents
- **MCP writes:** governed by token scope; write tools only for write-scoped clients, and every call is audited like a user call
- **MCP protocol:** spec 2026-07-28, falling back to 2025-11-25, with the official Go SDK `github.com/modelcontextprotocol/go-sdk` v1.8.x over stateless Streamable HTTP (v1.8.0 carries both protocol versions, `StreamableHTTPOptions.Stateless` and `CrossOriginProtection`, and the `auth`/`oauthex` metadata types)
- **later phases:** every LLM connection uses `github.com/azrtydxb/go-ai-sdk`. Phase 1 makes no LLM call, so it adds no LLM client.

## Problem

Hello can only be operated by a person in the console or by a script holding a user's all-powerful API token. AI agents (Claude Code, Claude Desktop, other MCP clients, and Hello's own agent in phase 2) need three things Hello lacks: an API description complete and precise enough to drive tools from (today 103 operations, 61 of them without a description, several documented with response codes they never send and missing ones they do, and two request bodies undocumented); a way for a user to grant an agent limited, revocable, expiring access without handing over a full-power token; and a standard agent interface (MCP) plus operating instructions (skills) so an agent can set up extensions, routing and phones or troubleshoot a call without guessing. Phase 2 builds on all three, so they come first.

## Users

- **Administrators:** connect an MCP client to Hello by pasting one URL, approve exactly which scopes it gets on a consent screen, see every connected app and service account, and revoke any of them at once; create service accounts for headless agents (CI, the talking-agent service later) with a secret shown once.
- **AI agents (MCP clients):** discover Hello's authorization server from a `401`, register by client ID metadata document, obtain a token with PKCE, list only the tools their scopes allow, call them with typed input and output schemas, read live calls, registrations and CDRs as resources, and follow skills that tell them how Hello is operated.
- **Script and integration authors:** keep using existing API tokens unchanged, create new tokens that are scoped and expiring, and rely on the OpenAPI document as the contract.
- **Operators:** see OAuth and MCP activity in metrics and audit rows that name the client, and trust that no secret leaves Hello through MCP.
- **Developers:** get a failing test whenever a route and the OpenAPI document drift apart in either direction.

## In scope

### OpenAPI completeness

- [S-1] **One route table, synced both ways with the document.** `internal/api` registers its routes from a single table (`routes(s) []route{Method, Pattern, Handler, Scope}`) instead of the inline `public`/`private` calls in `Handler`, so tests can enumerate it. A sync test fails if a registered route has no operation in `openapi.json`, an operation has no registered route, or the scope of a route differs from the operation's `x-hello-scope` (S-5). The existing check that every documented operation is routed (`TestVersionAndOpenAPI`) stays.
- [S-2] **Responses and request bodies conform.** In the `internal/api` tests (`internal/api/api_test.go` and its siblings), `Handler` is wrapped by a test-only validator (installed from `TestMain`) that checks every request the suite sends and every response it gets against the document: the status is documented for that operation; a JSON body validates against the documented schema (OpenAPI 3.1 is JSON Schema 2020-12; validated with `github.com/google/jsonschema-go`, which the MCP SDK already brings in); an operation whose handler reads a request body documents a `requestBody`, and a body the suite sends validates against it (JSON, or the documented multipart parts). At the end of the run every documented status of every operation must have been observed at least once, except the generic `401`, `500` and `503` (exempt everywhere) and entries in an explicit, commented exemption list that names why a status cannot be produced through fakes. A documented status nothing can produce is removed from the document; an undocumented one the suite produces is added.
- [S-3] **The known gaps are fixed** (each one becomes a conformance case of S-2):
  - `GET /api/v1/phones/{id}/preview` documents `422` `render_error` (the template fails for this phone);
  - `502` `upstream` is documented on `PUT /api/v1/extensions/{id}/voicemail`, `GET /api/v1/voicemail/messages/{id}/audio`, `GET /api/v1/recordings/{id}/audio`, `GET /api/v1/announcements/{id}/audio`, `POST /api/v1/announcements`, `PUT /api/v1/announcements/{id}` and `POST /api/v1/prov/firmware` (MinIO answered with an error);
  - `PUT /api/v1/extensions/{id}/voicemail` documents its request body as `oneOf` JSON (`application/json`, the box settings) or `multipart/form-data` (settings plus a `greeting` WAV part), and `POST /api/v1/voicemail/messages/{id}/heard` documents its optional body `{"heard": boolean}` (absent means `true`);
  - every documented `409` and `400` is either produced by a test (a uniqueness conflict, an in-use delete, a validation failure) or removed.
- [S-4] **Operations good enough to drive tools.** Every operation in `internal/api/openapi.json` has a unique camelCase `operationId` (already true), a `summary`, a `description` of at least one full sentence saying what it does and when to use it (written for an agent: side effects, what is returned, what is shown only once), a description on every parameter and every top-level request body property, and three extension fields:
  - `x-hello-scope`: `read`, `write`, `admin`, `secrets` or `session` (S-5);
  - `x-hello-mcp`: `true`, or an object `{"exclude": "<reason>"}` (S-14);
  - `x-hello-secret` on response schema properties that carry a show-once secret (device `secret` as in `deviceWithSecret` in `internal/api/handlers.go`, phone provisioning `url`/`token`, API and service-account secrets, revealed admin passwords), used by S-15 and by the redaction tests.
  - A lint test fails on any operation or parameter missing these.

### Authorization

- [S-5] **Scopes, enforced on the API** by `auth.Middleware` (`internal/auth/middleware.go`).
  - `read`: every `GET` except the ones below; `write`: configuration changes (extensions, devices, voicemail, recordings, announcements, ring groups, feature codes, trunks, routes, routing test, phones, templates, firmware, diagnostics clear); `admin`: credentials and the cluster (API tokens, service accounts, OAuth clients and grants of other users, vendor redirect credentials, node drain); `secrets`: operations whose purpose is to return a secret in plaintext (`POST /devices/{id}/rotate-secret`, `POST /phones/{id}/rotate-token`, `POST /phones/{id}/rearm`, `POST /phones/{id}/admin-password/reveal` and `/rotate`); `session`: consent approval and denial (S-8), which only a browser session may perform, so no token can grant itself scopes.
  - `admin` implies `write` implies `read`; `secrets` is orthogonal and must be granted explicitly. An operation that creates something and returns its show-once secret (device and phone create) needs `write` only.
  - A session cookie and a legacy API token (S-6) carry every scope.
  - The auth middleware, given the route's scope from the table, answers `403` `insufficient_scope` with `WWW-Authenticate: Bearer error="insufficient_scope", scope="<needed>", resource_metadata="<S-7 URL>"` when the caller lacks it.
  - The scopes a user may grant are bounded by `auth.GrantableScopes(user)`. Hello has no user roles today (every console user can do everything), so it returns all scopes; see Open questions.
- [S-6] **Tokens: prefixes, expiry, revocation, audit.**
  - Every new credential has a recognisable prefix, so secret scanners and log redaction can find it: `hello_pat_` (personal API token), `hello_at_` (OAuth access token), `hello_rt_` (refresh token), `hello_cs_` (service-account client secret), each followed by 32 random bytes base64url (the existing `auth.NewToken`). Only SHA-256 hashes are stored.
  - Existing API tokens (no prefix) keep working unchanged, with every scope, until revoked.
  - `POST /api/v1/tokens` gains optional `scopes` (absent means every scope except `secrets`, which must be named) and `expiresAt`; the list shows scopes, expiry, prefix kind, created and last used. Expired and revoked credentials answer `401`.
  - Revocation takes effect on the next request (credentials are looked up per request; nothing is cached across requests).
  - Audit rows of calls made with OAuth credentials carry the client in a new `via` column on `audit_events` (the client id); service accounts audit as `service:<name>`, users stay `user:<name>`, legacy tokens stay `token:<id>`. Every token issue, refresh, revocation, consent and service-account change writes an audit row.
- [S-7] **Discovery metadata and `401` challenges.** With `HELLO_PUBLIC_URL` set (e.g. `https://hello.kw.watteel.lab`; it is the issuer and the base of both resource identifiers):
  - `GET /.well-known/oauth-authorization-server` (RFC 8414): `issuer`, `authorization_endpoint`, `token_endpoint`, `revocation_endpoint`, `registration_endpoint` (only when DCR is on, S-10), `response_types_supported: ["code"]`, `grant_types_supported: ["authorization_code","refresh_token","client_credentials"]`, `code_challenge_methods_supported: ["S256"]`, `token_endpoint_auth_methods_supported: ["none","client_secret_basic","client_secret_post"]`, `scopes_supported`, `client_id_metadata_document_supported: true`, `authorization_response_iss_parameter_supported: true` (RFC 9207).
  - `GET /.well-known/oauth-protected-resource/mcp` and `/.well-known/oauth-protected-resource/api/v1` (RFC 9728): `resource` (`<public>/mcp`, `<public>/api/v1`), `authorization_servers: [<issuer>]`, `scopes_supported`, `bearer_methods_supported: ["header"]`, `resource_name`.
  - Every `401` from `/mcp` and `/api/v1` carries `WWW-Authenticate: Bearer resource_metadata="<the matching metadata URL>", scope="read"`.
  - Without `HELLO_PUBLIC_URL` the OAuth endpoints, the metadata and `/mcp` answer `404`, and the API works as today with sessions and API tokens.
- [S-8] **Authorization endpoint, client ID metadata documents and the consent screen** (the console's login, `web/src/pages/Login.tsx`, gains a return-to parameter).
  - `GET /oauth/authorize` accepts only `response_type=code` with `code_challenge` and `code_challenge_method=S256`, a `client_id`, a `redirect_uri`, optional `scope` (default `read`), `state` and one or more `resource` values (RFC 8707; each must be one of Hello's two resource identifiers, else `invalid_target`; absent binds the grant to both).
  - A `client_id` that is an `https://` URL with a path is a client ID metadata document: Hello fetches it (5 s timeout, 64 KiB cap, no redirects, `https` only, private and loopback addresses refused unless `HELLO_OAUTH_CIMD_ALLOW_PRIVATE=true`), requires its `client_id` to equal the URL, takes `client_name`, `client_uri`, `redirect_uris` and `token_endpoint_auth_method` (`none` only) from it, and caches it per its HTTP cache headers, at most 24 h. Other client ids must be registered (S-10, S-11).
  - The `redirect_uri` must match one registered for the client exactly, except that a loopback `http://127.0.0.1` or `http://[::1]` redirect may use any port (RFC 8252). Errors before the redirect URI is validated are shown on Hello's error page, never redirected.
  - A valid request is stored (`oauth_requests`, 10 min) and the browser is sent to the console at `/oauth/consent?request=<id>`; an unauthenticated user logs in first and returns there.
  - The consent screen shows the client name, the client id host prominently (the name is self-asserted, the host is not), the requested resources and each requested scope with a plain-language line, each pre-checked except `secrets` (unchecked, with a warning naming the operations it unlocks, the `secrets` rows of the route table in `internal/api/api.go`), and only scopes in `GrantableScopes(user)` selectable. The user may narrow the scopes. The page refuses framing (`frame-ancestors 'none'`).
  - Approve and deny are `POST /api/v1/oauth/requests/{id}/approve|deny` (scope `session`). Approve issues a single-use authorization code (60 s) and returns the redirect URL with `code`, `state` and `iss`; deny returns it with `error=access_denied`, `state` and `iss`.
- [S-9] **Token endpoint and refresh tokens.** `POST /oauth/token`:
  - `authorization_code`: verifies the code (single use; a second use revokes every token of its grant), the client, the `redirect_uri`, the PKCE verifier against the S256 challenge, and `resource` ⊆ the granted resources; issues an access token (`HELLO_OAUTH_ACCESS_TTL`, default 1 h) bound to the granted scopes and resources, and a refresh token.
  - `refresh_token`: rotates (the old refresh token is spent; reusing a spent one revokes the whole grant, OAuth 2.1 for public clients), may narrow scope, never widens it; refresh tokens expire after `HELLO_OAUTH_REFRESH_TTL` idle (30 d) and `HELLO_OAUTH_REFRESH_MAX` after the grant (90 d).
  - `client_credentials`: S-11.
  - Responses carry `Cache-Control: no-store`; errors are RFC 6749 JSON (`invalid_grant`, `invalid_client`, `invalid_scope`, `invalid_target`, …). Failed client authentications are throttled per client id and IP (10 per minute, then `429` with `Retry-After`).
  - An access token is accepted only for a resource in its audience: on `/api/v1` when bound to the API resource, on `/mcp` when bound to the MCP resource, and on `/api/v1` inside an MCP replay (S-15) when bound to the MCP resource. Anything else answers `401` `invalid_token`.
- [S-10] **Dynamic client registration (optional, deprecated).** `POST /oauth/register` (RFC 7591) exists only when `HELLO_OAUTH_DCR=true`. It registers public clients only (`token_endpoint_auth_method: none`), with the same redirect URI rules, rate-limited per IP (10 per hour), and marks them "unverified" on the consent screen; a registered client that never completes a grant within 24 h, or is unused for 30 days, is deleted. Whether kw turns it on is an open question.
- [S-11] **Service accounts and client credentials.** Administrators create service accounts (`/api/v1/service-accounts`, scope `admin`): a name, a description, the scopes it may hold (any, `secrets` included, bounded by `GrantableScopes` of the creating user), enabled. A service account has up to two client secrets at a time (for rotation), generated by `auth.NewToken`, each shown once with its prefix, optional expiry, last use, revocable. `POST /oauth/token` with `grant_type=client_credentials` and the secret (basic or post) issues an access token (1 h, no refresh token) with the requested scopes ⊆ the account's. Disabling an account or revoking a secret stops its tokens on the next request.
- [S-12] **Connected apps, revocation and the console** (token management moves out of `web/src/pages/System.tsx`; revoked credentials are matched by `auth.HashToken`, never stored in plaintext).
  - `POST /oauth/revoke` (RFC 7009) revokes an access or refresh token; revoking a refresh token revokes its grant.
  - `GET /api/v1/oauth/grants` lists the caller's grants (client, scopes, resources, created, last used); an `admin` sees every user's. `DELETE /api/v1/oauth/grants/{id}` revokes a grant and every token issued under it.
  - The console gains an **AI access** page (`/ai`): the MCP URL to copy, what each scope allows, connected apps with revoke, service accounts with their secrets (admin; shown once, as `web/src/pages/Devices.tsx` shows a device secret), personal API tokens moved here from System with their new scopes and expiry, and the skills download (S-18).

### MCP server

- [S-13] **The endpoint.** hello-control serves `POST /mcp` on its HTTP listener with the official Go SDK's Streamable HTTP handler (stateless, JSON responses), negotiating protocol version `2026-07-28` and falling back to `2025-11-25`; `GET` and `DELETE` answer `405` as the SDK does in stateless mode.
  - Authentication is a bearer token only (OAuth access token bound to the MCP resource, a service-account token, or a personal/legacy API token); a session cookie is ignored, so a browser page cannot drive MCP with a logged-in user's cookie. Missing or bad credentials answer `401` with the S-7 challenge.
  - Origin check: a request with an `Origin` header whose origin is not `HELLO_PUBLIC_URL`'s or listed in `HELLO_MCP_ALLOWED_ORIGINS` answers `403` (the SDK's `CrossOriginProtection` with those trusted origins). Requests without `Origin` (non-browser clients) pass.
  - Limits: 1 MiB request body, 30 s per tool call, server `instructions` naming the skills and the resources.
- [S-14] **Tools generated from the OpenAPI document.** At start, hello-control builds one tool per operation from the embedded `openapi.json`, except operations marked `x-hello-mcp: {"exclude": …}`:
  - name = `operationId`; title = `summary`; description = `description` plus the scope it needs;
  - `inputSchema`: an object with one property per path and query parameter (with their schemas and descriptions, path parameters required) and, when the operation has a JSON request body, a `body` property holding that schema with `$ref`s inlined (depth-limited);
  - `outputSchema`: the `2xx` JSON response schema when it is an object (Hello's list responses are `{items: […]}`), with `x-hello-secret` properties typed as string;
  - annotations: `readOnlyHint` for `GET`, `destructiveHint` for `DELETE`, `idempotentHint` for `PUT` and `DELETE`, `openWorldHint: false`.
  - Excluded, each with its reason in the document: `getOpenAPI`, `login`, `logout`; credential management (API tokens, service accounts, grants, consent); the `secrets`-scoped operations; binary and multipart operations (the three audio downloads, `cdrExport`, firmware upload, announcement create and replace); the multipart form of the voicemail PUT (its JSON form stays a tool); skills download.
  - `tools/list` returns only the tools the caller's scopes allow (write tools only with `write`). `tools/call` of a tool the caller's scopes do not allow answers HTTP `403` with the `insufficient_scope` challenge (MCP step-up); an unknown or excluded tool is a JSON-RPC invalid-params error.
- [S-15] **Execution by in-process replay.** A tool call becomes one request to the same `api.Handler` (method, path with escaped path parameters, query, JSON body), carrying the caller's `Authorization` header and a context marker (not a header, so no external request can forge it) that says "MCP replay of a token bound to the MCP resource" (S-9) and carries the client id for the audit `via`. Authentication, scope checks, validation, revision checks and audit are therefore exactly those of a direct API call by the same caller (the pattern of Nexora's `mgmt/internal/api/replay.go`, which replays through the full router with only the original credentials). A replayed request cannot start another replay.
  - Results: a `2xx` JSON object is returned as `structuredContent` and as its JSON text; an error returns `isError: true` with the API's `code`, `message` and `fields`. Text over 64 KiB is cut with a note saying how to page.
  - **Secrets never leave through MCP:** every `x-hello-secret` property in a result is replaced by `"[withheld: shown only in the Hello console]"` before it is returned, regardless of scope; the redaction runs on the decoded response, not on text.
  - Every tool call is logged (tool, actor, client, status, duration; never arguments or results) and counted (S-20); mutations write the API's audit rows with `via` set.
- [S-16] **Resources and prompts** over the live state in `internal/livestate` and the CDRs in `internal/cdr`.
  - Resources (scope `read`, read by replaying the matching `GET`): `hello://calls` (live calls), `hello://registrations`, `hello://cdrs/recent` (the last 50 CDRs), `hello://trunks/status`, `hello://cluster`; resource templates `hello://cdrs/{id}` (one CDR with its trace, as `getCDR` in `internal/api/cdr_handlers.go` returns it), `hello://extensions/{id}`, `hello://diagnostics/devices/{id}`. JSON, `application/json`. No subscriptions (stateless).
  - Prompts: `troubleshoot-call` (argument `number` or `cdrId`), `onboard-user` (`number`, `name`, optional `phoneMac` and `vendor`), `review-routing` (no arguments). Each prompt is a short instruction naming the tools and resources to use, consistent with the skills of S-17.

### Agent skills

- [S-17] **Skills in the repository**, checked against the tools derived from `internal/api/openapi.json`. `skills/` holds skills in the agentskills.io format: one folder per skill with a SKILL.md file (YAML frontmatter `name` equal to the folder name, lowercase with hyphens, at most 64 characters; `description` saying what it does and when to use it, at most 1024 characters; `license`; `metadata.hello-version`) and a `references/` folder. Phase 1 ships:
  - `hello-setup`: extensions, devices, voicemail, phone provisioning (adding a phone by MAC and model, binding it, what the administrator must still do in the console because MCP withholds show-once values, such as reading a device's SIP password in `web/src/pages/Devices.tsx`);
  - `hello-routing`: trunks, outbound and inbound routes, route order, ring groups, feature codes, and testing with `routingTest` before and after a change;
  - `hello-troubleshoot`: a call that failed (CDR and trace), a phone that does not register (registrations, device diagnostics, auth failures), a trunk that is down, and what the cluster state means.
  - Each skill says how to connect (the MCP URL and scopes it needs), refers to tools by their exact names in inline code, and keeps its SKILL.md under 500 lines with detail in `references/`. Skills are embedded in the hello-control binary (`skills/skills.go`, `//go:embed`).
  - `TestSkills` (package `skills`) fails if a frontmatter field is missing or invalid, `name` differs from the folder, a relative link points at a missing file, or an inline-code identifier in camelCase (the documented convention for naming a tool) is not a tool hello-control's MCP server exposes.
- [S-18] **Skills download.** `GET /api/v1/skills` (scope `read`) lists each skill's name, description and version; `GET /api/v1/skills/{name}/download` returns the folder as a zip (`application/zip`, `Content-Disposition` with `<name>.zip`), ready to unpack into a client's skills directory. The AI access page lists them with download buttons and a one-line install hint per client.

### Deployment, observability and docs

- [S-19] **kw.** `hello.kw.watteel.lab` gets TLS (cert-manager `Certificate` `hello-tls` from `cluster-ca`, the issuer the provisioning host already uses; HTTP redirects to HTTPS); the UI's nginx proxies `/mcp`, `/oauth/` and `/.well-known/oauth-` to hello-control as it proxies `/api/`; hello-control gets `HELLO_PUBLIC_URL=https://hello.kw.watteel.lab`. The compose lab gets the same paths with a lab certificate.
- [S-20] **Metrics and secret hygiene** (secrets as in `internal/secret`, redaction as in `telemetry.RedactURL`), registered on the `internal/telemetry` registry: `hello_oauth_tokens_issued_total{grant}`, `hello_oauth_token_failures_total{reason}`, `hello_oauth_cimd_fetches_total{result}`, `hello_mcp_requests_total{method,result}`, `hello_mcp_tool_calls_total{tool,result}`, `hello_mcp_tool_call_seconds`. No access token, refresh token, code, verifier, client secret or `x-hello-secret` value appears in a log line, metric label, audit row or MCP result; `TestNoSecretsInLogs` covers them, and log redaction masks the `hello_*_` prefixes.
- [S-21] **Docs.** A new guide, docs/ai-access.md, written like `docs/provisioning.md` and linked from `README.md`: connecting an MCP client (Claude Code, Claude Desktop and any client that speaks Streamable HTTP; trusting `cluster-ca`), the scopes, consent and revocation, service accounts, the tool and resource list (generated from the document by a test-checked script), installing the skills, and the security model (secrets withheld, audit `via`). The README links it.
- [S-22] **End to end.** An integration test uses the MCP SDK's client with its OAuth authorization-code handler against a running hello-control: it gets the `401`, reads both metadata documents, authorizes a client ID metadata document served by the test, approves consent through the API with a session, exchanges the code, lists tools (read-only first, then with `write`), creates and reads back an extension through tools, reads `hello://registrations`, refreshes the token, revokes the grant, and is refused on its next call.

## Out of scope

- The in-product agent (phase 2) and voice agents (phase 3); any LLM call.
- User roles and per-object permissions beyond scopes (see Open questions).
- Exposing Hello to the internet: clients must reach `hello.kw.watteel.lab` from the LAN and trust `cluster-ca`; cloud-hosted clients (for example claude.ai's web connectors) that cannot reach the lab are not supported.
- OpenID Connect (ID tokens, userinfo), token introspection (RFC 7662), JWT access tokens, DPoP and mutual-TLS-bound tokens, `private_key_jwt` client authentication, pushed authorization requests.
- MCP sampling, elicitation, roots, resource subscriptions, server-initiated notifications, tasks; the SSE `GET` stream (stateless mode).
- A stdio bridge: clients that only speak stdio use a generic Streamable HTTP bridge.
- Audio and file content through MCP (binary operations are excluded).
- Generating server code or clients from the OpenAPI document; it stays hand-written and test-checked.

## Constraints

- **Dependencies:** one new direct dependency, `github.com/modelcontextprotocol/go-sdk` v1.8.x (bringing `github.com/google/jsonschema-go`, also used for S-2). No OAuth library: the authorization server is small, stdlib-only, and `oauthex` supplies the metadata types.
- **Security:** OAuth 2.1 only (no implicit or password grant, PKCE S256 mandatory, exact redirect URIs, single-use codes, rotating refresh tokens with reuse detection); credentials hashed at rest; every OAuth and MCP endpoint over HTTPS on kw; CIMD fetches SSRF-guarded; the consent page is not frameable; approval needs a browser session (scope `session`), so no token can widen its own or another token's scopes; MCP never returns a secret.
- **Compatibility:** existing sessions, API tokens, routes, request and response shapes are unchanged (documentation fixes only change the document, except the additive token fields and new routes). A deployment without `HELLO_PUBLIC_URL` behaves as today.
- **Performance:** a credential lookup is one indexed query, as today; tool generation happens once at start; a tool call adds under 2 ms p99 over the replayed handler (benchmark, `HELLO_BENCH=1`).
- **HA:** both hello-control replicas serve OAuth and MCP; all OAuth state is in PostgreSQL, throttles in Valkey with the in-memory fallback the provisioning limiter uses; the MCP server is stateless.
- **Deployment:** kw via Kuvryn Sync; CI on the Arc runners; nothing runs on the user's Mac.
- **Gate per task:** gofmt, `go vet`, golangci-lint 0, `go test -race ./...`, `procoder check` 0 blocking, `procoder test` and `procoder lint` over `web/`, mutation checks on the security-relevant branches; REVIEW.md applies.

## Interfaces

- **Env (hello-control):** `HELLO_PUBLIC_URL` (enables OAuth and MCP; `https://` scheme and host only, `http://localhost…` accepted for tests), `HELLO_OAUTH_ACCESS_TTL` (1h), `HELLO_OAUTH_REFRESH_TTL` (30d), `HELLO_OAUTH_REFRESH_MAX` (90d), `HELLO_OAUTH_DCR` (false), `HELLO_OAUTH_CIMD_ALLOW_PRIVATE` (false), `HELLO_MCP_ALLOWED_ORIGINS` (comma-separated origins, empty).
- **HTTP, OAuth (not under `/api/v1`, documented in the new guide of S-21):** `GET /.well-known/oauth-authorization-server`, `GET /.well-known/oauth-protected-resource/mcp`, `GET /.well-known/oauth-protected-resource/api/v1`, `GET /oauth/authorize`, `POST /oauth/token`, `POST /oauth/revoke`, `POST /oauth/register` (DCR on only).
- **HTTP, MCP:** `POST /mcp` (Streamable HTTP, stateless).
- **HTTP, management API (all in `openapi.json`):**
  - `GET /api/v1/oauth/requests/{id}`, `POST /api/v1/oauth/requests/{id}/approve` (`{scopes: [...]}`), `POST /api/v1/oauth/requests/{id}/deny`
  - `GET /api/v1/oauth/grants`, `DELETE /api/v1/oauth/grants/{id}`
  - `GET|POST /api/v1/service-accounts`, `GET|PATCH|DELETE /api/v1/service-accounts/{id}`, `POST /api/v1/service-accounts/{id}/secrets`, `DELETE /api/v1/service-accounts/{id}/secrets/{secretId}`
  - `POST /api/v1/tokens` gains `scopes` and `expiresAt`; token list items gain `scopes`, `expiresAt`, `kind`
  - `GET /api/v1/skills`, `GET /api/v1/skills/{name}/download`
  - `GET /api/v1/ai/settings` (public URL, MCP URL, metadata URLs, protocol versions, DCR on/off, scopes with descriptions)
- **OpenAPI extensions:** `x-hello-scope`, `x-hello-mcp`, `x-hello-secret` (S-4).
- **MCP:** tools named by `operationId`; resources `hello://…` (S-16); prompts `troubleshoot-call`, `onboard-user`, `review-routing`.
- **UI routes:** `/ai` (AI access), `/oauth/consent`.
- **Files:** `skills/<name>/SKILL.md`, `skills/<name>/references/*.md`, and the new guide of S-21.
- **kw:** `Certificate` `hello-tls`, the `hello` Ingress with TLS, nginx locations for `/mcp`, `/oauth/`, `/.well-known/oauth-`.

## Data

- **PostgreSQL** (migration `00008_ai_access.sql`, owned by hello-control):
  - `api_tokens` gains `scopes text[] null` (null = legacy, every scope), `expires_at timestamptz null`, `revoked_at timestamptz null`, `kind text not null default 'legacy'` (`legacy` or `personal`).
  - `oauth_clients`: `client_id text primary key` (the CIMD URL, `hello_dcr_<random>`, or `hello_sa_<id>`), `kind` (`cimd`, `dcr`, `service`), `name`, `client_uri`, `redirect_uris text[]`, `metadata jsonb`, `fetched_at`, `cache_until`, `created_at`, `last_used_at`; for `service` also `description`, `scopes text[]`, `enabled`, `created_by`.
  - `oauth_client_secrets`: `id`, `client_id` references `oauth_clients` on delete cascade, `secret_hash bytea unique`, `created_at`, `expires_at`, `revoked_at`, `last_used_at`; at most two unrevoked per client (checked in the transaction).
  - `oauth_requests`: `id text primary key` (random), `client_id`, `redirect_uri`, `state`, `code_challenge`, `scopes text[]`, `resources text[]`, `created_at`, `expires_at`, `user_id null`, `code_hash bytea unique null`, `code_expires_at`, `code_used_at`.
  - `oauth_grants`: `id`, `user_id` references `users` on delete cascade, `client_id`, `scopes text[]`, `resources text[]`, `created_at`, `last_used_at`, `revoked_at`.
  - `oauth_tokens`: `hash bytea primary key`, `kind` (`access`, `refresh`), `grant_id null` (null for client credentials), `client_id`, `user_id null`, `scopes text[]`, `resources text[]`, `created_at`, `expires_at`, `spent_at null` (refresh rotation), `revoked_at null`; index on `(grant_id)` and `(expires_at)`; a daily prune of rows expired more than 7 days.
  - `audit_events` gains `via text null`.
- **Valkey:** `hello:oauth:rl:client:{id}:{ip}`, `hello:oauth:rl:register:{ip}` (sliding windows, TTL).
- **Binary:** `openapi.json` (already embedded) and `skills/` embedded; the tool set is derived at start, never stored.

## Edge cases

- A client sends `resource` with a trailing slash or a different case in the host: compared after RFC 3986 normalisation; anything else is `invalid_target`.
- The CIMD document changes its `redirect_uris` between authorize and token: the authorization request keeps the URI it validated; a new authorize refetches when the cache expired.
- The CIMD URL redirects, is slow, is huge, resolves to a private address, or its `client_id` differs from the URL: refused with a clear error page, nothing stored.
- The user narrows the scopes on the consent screen: the token gets the narrowed set and the token response's `scope` says so; a client that then calls a write tool gets the step-up `403`.
- Two token requests race on one authorization code, or on one refresh token: one succeeds; the other gets `invalid_grant` and (for refresh reuse) the grant is revoked — so a stolen refresh token used after the real client's refresh kills the grant rather than continuing silently.
- A user deletes a personal token while an agent is mid-way through a multi-call task: the next call is `401` and the agent must re-authorize.
- The user who approved a grant is deleted: the grant and its tokens go with it (cascade).
- A tool's response holds a secret nested in a list (devices created in bulk later): the redaction walks the decoded JSON by schema, not by top-level field name.
- An operation's JSON schema uses `oneOf` (the voicemail PUT): the tool's `body` takes the JSON alternative only.
- A legacy token is used on `/mcp`: allowed, with every scope but `secrets` filtered as for any caller (MCP excludes `secrets` tools anyway).
- An MCP client sends `Origin: null` or a browser extension origin: refused unless listed.
- `HELLO_PUBLIC_URL` differs from the Host the client used (an IP instead of the name): metadata and `iss` always use `HELLO_PUBLIC_URL`; the token's audience is checked against it, not against the Host header.
- A document change renames an `operationId`: tool names change; the skills test fails until the skills follow, so a release never ships a skill that names a missing tool.

## Failure modes

- **PostgreSQL unavailable:** OAuth and MCP answer `503` (the token endpoint with an RFC 6749 `temporarily_unavailable` body); nothing is cached in memory that would admit a revoked credential.
- **Valkey unavailable:** throttles fall back to per-replica memory limits; nothing else depends on it.
- **The CIMD host is unreachable or kw has no egress to it:** authorization fails with an error page naming the fetch failure; already-issued tokens keep working until they expire; a cached document is used while its cache entry lasts.
- **A replayed operation panics or times out:** the tool result is `isError` `internal` or `timeout`; the HTTP request itself still answers; the API's own recovery and audit behave as for a direct call.
- **The MCP SDK rejects a protocol version a client asks for:** the SDK answers with its supported version; the client decides; a test pins both supported versions.
- **The OpenAPI document is malformed at build:** hello-control refuses to start (tool generation fails loudly) and the sync test fails first in CI.
- **TLS on `hello.kw.watteel.lab` is not trusted by a client machine:** the client cannot connect at all; the new guide (S-21) covers installing `cluster-ca`.

## Acceptance criteria

- [ ] [S-1] `TestRoutesMatchOpenAPI` in `internal/api` passes. It fails if a route in the table has no operation, an operation has no route, or a route's scope differs from the operation's `x-hello-scope`.
- [ ] [S-2] [S-3] `TestOpenAPIConformance` in `internal/api` passes over the whole package suite. It fails if any response has an undocumented status or a body that does not validate, any request body the suite sends does not validate or reaches an operation without a `requestBody`, or a documented status (outside `401`, `500`, `503` and the commented exemptions) is never observed — including `422` on preview, `502` on each listed MinIO-backed operation, both voicemail PUT body forms and the heard body.
- [ ] [S-4] `TestOpenAPIForTools` in `internal/api` passes. It fails if an operation lacks a summary, a description of at least one sentence, a description on any parameter or top-level body property, `x-hello-scope`, or `x-hello-mcp`, or if a response property carrying a show-once secret lacks `x-hello-secret`.
- [ ] [S-5] `TestScopeEnforcement` in `internal/api` passes. For every operation it fails if a token whose scopes are just below the operation's is admitted, if the operation's own scope (or a higher one) is refused, if the `403` lacks the `insufficient_scope` challenge, if a bearer token can call a `session` operation, or if a session or legacy token is refused anything.
- [ ] [S-6] `TestTokenLifecycle` in `internal/auth` and `internal/store` passes. It fails if a new credential lacks its prefix, a plaintext credential is stored, a legacy token stops working or loses a scope, `POST /tokens` without `scopes` grants `secrets`, an expired or revoked credential is admitted, or an OAuth call's audit row lacks `via`.
- [ ] [S-7] `TestOAuthMetadata` in `internal/oauth` passes. It fails if either metadata document lacks a field listed in S-7 or names another issuer or resource, if a `401` from `/mcp` or `/api/v1` lacks `resource_metadata`, or if any OAuth or MCP path answers other than `404` without `HELLO_PUBLIC_URL`.
- [ ] [S-8] `TestAuthorizeAndConsent` in `internal/oauth` passes. It fails if a request without S256 PKCE, with an unregistered or inexact redirect URI, or with a foreign `resource` is accepted; if an error before redirect-URI validation redirects; if a CIMD document that redirects, exceeds 64 KiB, times out, resolves to a private address or names another `client_id` is accepted; if approval issues a scope the user narrowed away or outside `GrantableScopes`; if the redirect lacks `iss` or `state`; or if a bearer token can approve.
- [ ] [S-8] [S-12] `procoder test` and `procoder lint` pass over `web/`. `Consent.test.tsx` fails if `secrets` is pre-checked, a non-grantable scope is selectable, the client host is not shown, or deny does not redirect with `access_denied`; `AIAccess.test.tsx` fails if a service-account secret or personal token is shown other than once, revoking a grant does not call the API, or the MCP URL is not the one `GET /api/v1/ai/settings` returns.
- [ ] [S-9] `TestTokenEndpoint` in `internal/oauth` passes. It fails if a wrong PKCE verifier, a reused or expired code, a mismatched redirect URI or client is accepted; if a reused code does not revoke the grant's tokens; if a refresh token is not rotated, a spent one does not revoke the grant, or a refresh widens scope; if the idle or absolute refresh limit is not enforced; if a token is accepted outside its audience (an MCP-bound token directly on `/api/v1`), or an MCP-bound token is refused inside a replay; or if responses lack `no-store`.
- [ ] [S-9] [S-10] `TestOAuthThrottle` in `internal/oauth` passes with Valkey and with Valkey stopped. It fails if the eleventh failed client authentication in a minute or the eleventh registration in an hour from one IP is not `429`.
- [ ] [S-10] `TestDynamicRegistration` in `internal/oauth` passes. It fails if `/oauth/register` exists with DCR off, registers a confidential client, accepts a non-loopback `http` redirect, or keeps a client unused past the cleanup rules.
- [ ] [S-11] `TestServiceAccounts` in `internal/api` and `internal/oauth` passes. It fails if a secret is returned other than once, a third unrevoked secret is allowed, a client-credentials token gets a scope outside the account's or a refresh token, or a disabled account or revoked secret still works on the next request.
- [ ] [S-12] `TestGrantsAndRevocation` in `internal/oauth` passes. It fails if revoking a refresh token or deleting a grant leaves any of its tokens working, a user can see or revoke another user's grant without `admin`, or a revocation writes no audit row.
- [ ] [S-13] `TestMCPTransport` in `internal/mcp` passes. It fails if `initialize` with `2026-07-28` or `2025-11-25` does not negotiate that version, a session cookie alone is admitted, a foreign `Origin` is not `403`, an unauthenticated request lacks the challenge, or a body over 1 MiB is read.
- [ ] [S-14] `TestToolsFromOpenAPI` in `internal/mcp` passes. It fails if any non-excluded operation has no tool or a tool has no operation, a tool's input or output schema differs from the operation's (parameters, required path parameters, body), annotations are wrong for the method, an excluded operation is listed, `tools/list` shows a write tool without `write`, or calling a tool beyond the caller's scope does not get the step-up `403`.
- [ ] [S-15] `TestToolReplay` in `internal/mcp` passes. It fails if a tool call does not reach the handler with the caller's credentials and the replay marker, an external request carrying any header can pose as a replay, a replay can replay, a mutation's audit row lacks `via`, an API error is not returned as `isError` with code and fields, or any `x-hello-secret` value (device create included) appears in a result.
- [ ] [S-16] `TestResourcesAndPrompts` in `internal/mcp` passes. It fails if a listed resource or template does not return its `GET` operation's body, works without `read`, or a prompt names a tool or resource that does not exist.
- [ ] [S-17] `TestSkills` in `skills` passes. It fails if a skill's frontmatter is missing or invalid, its name differs from its folder, `SKILL.md` exceeds 500 lines, a relative link is broken, or a camelCase inline-code identifier is not an exposed MCP tool.
- [ ] [S-18] `TestSkillsDownload` in `internal/api` passes. It fails if the list differs from the embedded skills, a download is not a zip whose files equal the skill folder, an unknown name is not `404`, or the download works without `read`.
- [ ] [S-19] `TestKwAIAccess` in `test/deploy` passes. It fails if the kw manifest lacks the `hello-tls` certificate from `cluster-ca` on the `hello` Ingress, `HELLO_PUBLIC_URL` on hello-control, or the nginx template lacks the `/mcp`, `/oauth/` and `/.well-known/oauth-` locations.
- [ ] [S-20] `TestOAuthMCPMetrics` in `internal/mcp` passes, and `TestNoSecretsInLogs` also covers OAuth and MCP. They fail if a token issue, a token failure, a CIMD fetch, an MCP request or a tool call does not move its metric, or if any access token, refresh token, code, verifier, client secret or withheld value appears in a log line after the end-to-end test.
- [ ] [S-21] `docs/ai-access.md` exists and covers every item of S-21; `TestDocsAIAccess` in `test/deploy` fails if its tool and resource tables differ from what hello-control generates, or the README does not link it.
- [ ] [S-22] `TestMCPEndToEnd` in `test/integration` passes. It fails at the first step of S-22 that does not behave as described, including the refusal after revocation.
- [ ] [S-15] `TestToolCallOverhead` in `internal/mcp` (`HELLO_BENCH=1`) passes. It fails if a tool call's p99 latency exceeds the same direct API call's p99 by 2 ms or more.

## Open questions

- OPEN: User roles. Hello has one kind of user, who can do everything, so "the user's role bounds the scopes on the consent screen" bounds nothing today. Should phase 1 add roles (for example administrator, operator, read-only, applied to the console and the API too), or keep one role so every user can grant every scope including `secrets`? Proposed: keep one role in phase 1 (`GrantableScopes` is the single place a later roles spec plugs in).
- OPEN: Auditing MCP reads. "Every call is audited like a user call": a user's reads write no audit row today. Should MCP read tool calls write audit rows too, or is a log line plus metrics per call (and the normal audit rows for every change) enough? Proposed: log line plus metrics for reads, audit rows for changes, as for users.
- OPEN: Dynamic client registration on kw. Client ID metadata documents are the preferred registration and DCR is deprecated, but some MCP clients still only register dynamically. Should `HELLO_OAUTH_DCR` be on for kw? Proposed: on, so every client works today, with the rate limit, the "unverified" consent label and the cleanup rules of S-10.
