# minimum-pbx — implementation plan

Status: draft
Spec: .procoder/specs/minimum-pbx.md

## Goal

Two SIP phones register with Hello over SIP/UDP through either lab node and call each other through a B2BUA. Administrators provision them through an authenticated API and UI and see live registrations, calls and CDRs.

## Architecture

Three parallel streams share fixed contracts committed before they start: the PostgreSQL schema (`migrations/00002_minimum_pbx.sql`), the Valkey live-state package (`internal/livestate`), the configuration structs (`internal/config`), and the HTTP JSON shapes below. hello-control owns the schema and the management API (Task 2). hello-sip owns SIP handling, reading a revisioned in-memory snapshot of devices and writing bindings, calls and CDRs (Task 3). The UI consumes the API (Task 4). Task 5 integrates them in the lab with Go test user agents.

## Constraints

- No PostgreSQL call on the SIP request path; Valkey calls on it use `config.SIP.StateTimeout`.
- Secrets — device SIP secrets, user passwords, API tokens, `HELLO_SIP_NONCE_SECRET` — never appear in logs, API responses after creation, metrics or CDRs; Authorization headers are stripped from any logged SIP message.
- New dependencies limited to `github.com/emiago/sipgo` v1.6.0 and `golang.org/x/crypto` (bcrypt); both already in `go.mod`.
- UDP only. Stdlib `net/http` routing and `log/slog`, as in Phase 0.
- Every task: `gofmt`, `go vet ./...`, `golangci-lint run ./...`, and `go test -race ./...` clean; `procoder check` clean before its branch is handed back.

### Shared contracts (fixed; a stream that needs a change asks the lead, never edits another stream's files)

Configuration revision, written by hello-control in the same transaction as any extension or device change:

```sql
UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision;
SELECT pg_notify('hello_config', <revision>::text);
```

hello-sip's snapshot query (read-only; `realm` must equal `HELLO_SIP_DOMAIN`, otherwise the device is skipped and logged):

```sql
SELECT (SELECT config_revision FROM schema_info),
       d.id, d.sip_username, d.realm, d.ha1_md5, d.ha1_sha256, e.number, e.name
FROM extensions e LEFT JOIN devices d ON d.extension_id = e.id AND d.enabled;
-- Rows with NULL device columns are extensions without an enabled device:
-- known numbers, so a call to them is 480, not 404 (amended 2026-10-02).
```

The AOR of a device is `sip:<sip_username>@<HELLO_SIP_DOMAIN>`. A call is to an extension number; it rings every binding of every enabled device of that extension. Digest HA1 is `hex(H(sip_username ":" realm ":" secret))` for MD5 and SHA-256. A secret is 24 random bytes, base64url without padding.

HTTP JSON (camelCase; timestamps RFC 3339; errors are `{"error":{"code":"<bad_request|unauthorized|not_found|conflict|internal>","message":"..."}}` with 400/401/404/409/500; lists are `{"items":[...]}`):

- `POST /api/v1/auth/login` `{"username","password"}` → 204 with cookie `hello_session` (HttpOnly, SameSite=Strict, Path=/, Secure when the request is HTTPS), or 401. `POST /api/v1/auth/logout` → 204. `GET /api/v1/auth/me` → `{"username"}`.
- `GET /api/v1/tokens` → items `{"id","name","createdAt","lastUsedAt"}`; `POST` `{"name"}` → 201 with the same plus `"token"` (shown once); `DELETE /api/v1/tokens/{id}` → 204. Bearer header: `Authorization: Bearer <token>`.
- Extension `{"id","number","name","createdAt","updatedAt"}`. `POST /api/v1/extensions` `{"number","name"}` → 201; `PATCH` with either field → 200; `DELETE` → 204 (cascades to devices).
- Device `{"id","extensionId","sipUsername","enabled","createdAt","updatedAt"}`. `POST /api/v1/devices` `{"extensionId","sipUsername","enabled"?}` → 201, the device plus `"secret"`; `PATCH` `{"enabled"?,"extensionId"?}` → 200; `DELETE` → 204; `POST /api/v1/devices/{id}/rotate-secret` → 200, the device plus `"secret"`.
- `GET /api/v1/registrations` → items of `livestate.Binding`. `GET /api/v1/calls` → items of `livestate.Call`.
- `GET /api/v1/cdrs?before=<id>&limit=<1..200, default 50>` → `{"items":[CDR...],"next":"<id or empty>"}`. CDR: `{"id","correlationId","sipCallId","source","destination","startTime","ringTime","answerTime","endTime","durationMs","billableMs","sipNode","mediaMode","finalStatus","terminationSide","failureReason"}`; null times are omitted.
- `GET /api/v1/version` → `{"version","commit","configRevision"}`, the revision now read from `schema_info`.

## Task 1: Shared contracts

Files: `migrations/00002_minimum_pbx.sql` (schema), `internal/livestate/livestate.go` and `livestate_test.go` (Valkey bindings and calls), `internal/config/config.go` and `config_test.go` (Phase 1 settings), `.procoder/plans/minimum-pbx.md` (this file).
Interfaces: `livestate.New(valkey.Client) *Store` with `PutBinding`, `DeleteBinding`, `DeleteAOR`, `Bindings`, `AllBindings`, `PutCall`, `DeleteCall`, `Calls`; `config.Control{ValkeyAddr, SIPDomain, BootstrapAdminPassword, SessionTTL}`; `config.SIP{DatabaseURL, Database, SIPDomain, NonceSecret, RegisterMinExpires, RegisterMaxExpires, RingTimeout, AuthFailLimit, AuthFailWindow, StateTimeout}`.

- [x] Write the migration and confirm it applies: `go test ./test/integration/ -run Migrate` with `HELLO_TEST_DATABASE_URL` set → ok.
- [x] Write `internal/livestate` and run `HELLO_TEST_VALKEY_ADDR=… go test ./internal/livestate/` → `TestBindingsRefreshAndExpiry` and `TestCallsTTL` pass against Valkey 9.
- [x] Extend `internal/config`, then run `go test ./internal/config/` → ok.
- [x] Commit to `phase-1-minimum-pbx` and branch `phase-1-control`, `phase-1-sip` and `phase-1-ui` from that commit, each in its own worktree.

## Task 2: Control plane (branch phase-1-control)

Files: `internal/auth/` (bcrypt passwords, session and token hashing, bootstrap admin, middleware), `internal/store/` (PostgreSQL queries for users, sessions, tokens, extensions, devices, audit, cdrs, revision), `internal/api/` (handlers, OpenAPI document, tests), `cmd/hello-control/main.go` (wiring, Valkey client for live views, bootstrap on serve).
Interfaces: produces every HTTP route in Shared contracts; consumes `livestate.Store` for registrations and calls; writes the revision bump and NOTIFY.

- [ ] Implement `internal/auth`: `HashPassword`, `CheckPassword` (bcrypt), `NewToken() (plain string, hash []byte)` using 32 random bytes and SHA-256, and `Middleware(store) func(http.Handler) http.Handler`, which accepts the session cookie or a Bearer token, updates `last_used_at`, and puts the actor in the request context. Test it in `internal/auth/auth_test.go`.
- [ ] Implement `internal/store`, with every mutation in one transaction that also inserts the audit row and runs the revision bump plus `pg_notify`. Map unique violations to a sentinel `store.ErrConflict` and missing rows to `store.ErrNotFound`.
- [ ] Implement the handlers and add every route and schema to `internal/api/openapi.json`. Keep `TestVersionAndOpenAPI` passing and extend it to assert that every documented path is routed.
- [ ] Write `TestAuthRequired`, `TestLoginSession`, `TestAPITokenHashed` and `TestDeviceSecretShownOnce` in `internal/api`. They need PostgreSQL through `HELLO_TEST_DATABASE_URL` and skip without it; each runs in a scratch database the same way `test/integration` already does.
- [ ] Write `TestConfigChangeAuditedAndRevisioned` in `test/integration`.
- [ ] Bootstrap: on `serve`, when `users` is empty and `HELLO_BOOTSTRAP_ADMIN_PASSWORD` is set, create user `admin`. Log that it happened, never the password. Prune expired sessions hourly.
- [ ] Run `go test -race ./...` with PostgreSQL and Valkey set (all pass), then `golangci-lint run ./...` (0 issues), then `procoder check` (clean).

## Task 3: SIP node (branch phase-1-sip)

Files: `internal/sip/` (sipgo server and client behind Hello types, digest and nonce, registrar, B2BUA, failure mapping, metrics, log redaction), `internal/snapshot/` (load, LISTEN/NOTIFY, poll, last-good retention), `internal/cdr/` (bounded queue and background PostgreSQL writer), `cmd/hello-sip/main.go` (wiring, readiness requiring a loaded snapshot plus Valkey).
Interfaces: consumes the snapshot query, `livestate.Store` and `config.SIP`; produces SIP on UDP, the `cdrs` rows, `hello_sip_*`/`hello_calls_total`/`hello_active_calls`/`hello_cdr_dropped_total` metrics, and `livestate` bindings and calls (heartbeat every 10s, TTL 30s).

- [ ] Implement `internal/snapshot`: `Load(ctx, db) (*Snapshot, error)`, `Snapshot.DeviceByUsername`, `Snapshot.DevicesForExtension(number)`, and `Watcher.Run(ctx)`, which LISTENs on `hello_config` and polls every 30s. Retain the last good snapshot on error and expose `Ready() bool`.
- [ ] Implement digest in `internal/sip/digest.go`: the challenge uses a stateless nonce `base64(ts || HMAC-SHA256(secret, ts||realm))` valid for 5 minutes; verify MD5 and SHA-256 (RFC 8760), with `qop=auth` and `stale=true` on expiry. Write `TestDigestMD5AndSHA256`, `TestNonceAcrossNodes` and `TestStaleNonce`.
- [ ] Implement the registrar: authenticate, clamp Expires (423 with Min-Expires below the minimum), handle `Expires: 0` and `Contact: *`, store through `livestate.PutBinding` using `rport`/`received` as the source, and answer 200 with the current bindings.
- [ ] Implement the failed-auth throttle with Valkey `INCR` and `EXPIRE` on `hello:authfail:{ip}`, answering 403 once the count passes the limit.
- [ ] Implement the B2BUA: authenticate the INVITE (401), resolve the dialled extension, fork to every binding (ring all) excluding the caller's own device, take the first 2xx (ACK and BYE any later 2xx, CANCEL the other forks), relay ACK, BYE, CANCEL and in-dialog re-INVITE/UPDATE between legs, and pass SDP through unchanged. Apply the 404/480/486/408 mapping from the spec. Publish to `livestate` on ring and answer, and remove on end.
- [ ] Implement the CDR writer: a bounded channel (1000) with a writer goroutine that retries with backoff; when the channel is full, drop and increment `hello_cdr_dropped_total`.
- [ ] Write `TestOptionsPing` and `TestSIPMetrics` in `internal/sip`, plus unit tests for fork and cancel races using in-process sipgo UAs on loopback.
- [ ] Run `go test -race ./...` (pass), then `golangci-lint run ./...` (0 issues), then `procoder check` (clean).

## Task 4: UI (branch phase-1-ui)

Files: `web/src/` (api client, auth context, `Login`, `Extensions`, `Devices`, `Registrations`, `Calls`, `History` pages, and tests).
Interfaces: consumes only the HTTP JSON in Shared contracts.

- [ ] Extend `web/src/api.ts` with typed calls for every route. On a 401, redirect to `/login?next=<path>`.
- [ ] Build the Login page and auth context (`GET /api/v1/auth/me` on load) and a logout button in the nav.
- [ ] Build the Extensions and Devices pages: list, create, edit and delete. The secret from create and rotate appears once, in a dialog with a copy button, and is cleared from state when the dialog closes or the page changes.
- [ ] Build the Registrations and Active Calls pages (refreshed every 5s), and Call History with "older" paging using `next`.
- [ ] Write `Devices.test.tsx` and `Login.test.tsx` with the criteria from the spec. Run a mutation check on each: remove the behaviour, confirm the test fails, restore.
- [ ] Run `pnpm typecheck`, `pnpm lint`, `pnpm test` and `pnpm build` (all pass) and `procoder check` (clean).

## Task 5: Integration, lab and docs (branch phase-1-minimum-pbx, after merging Tasks 2–4)

Files: `test/sipua/` (Go test user agent: register, call, answer, busy, hang up), `test/integration/*_test.go` (the spec's lab-level criteria), `deploy/docker-compose/compose.yaml` (SIP ports, new env), `docs/phones.md`, `README.md`.
Interfaces: consumes everything above.

- [ ] Merge `phase-1-control`, `phase-1-sip` and `phase-1-ui` into `phase-1-minimum-pbx`, resolving conflicts hunk by hunk. Then run `go test -race ./...` (pass).
- [ ] Write `test/sipua` on sipgo, and the integration tests `TestRegisterBindings`, `TestAuthFailThrottle`, `TestCallRingAllAndHangup`, `TestCallAcrossNodes`, `TestCallFailureCodes`, `TestLiveRegistrationsAndCalls`, `TestCDRWritten`, `TestSnapshotReloadOnNotify`, `TestSnapshotSurvivesDatabaseLoss` and `TestNoSecretsInLogs` against the compose lab (`HELLO_DOCKER=1`).
- [ ] Update compose: UDP ports 5060 and 5062, `HELLO_SIP_DOMAIN`, the nonce secret, the bootstrap password and the database URL for hello-sip. Extend `TestLabSmoke` to register two UAs and complete a call.
- [ ] Write `docs/phones.md` and update the README configuration table. Run `HELLO_DOCKER=1 go test -timeout 20m ./test/integration/` (pass) and `procoder check` (clean).
