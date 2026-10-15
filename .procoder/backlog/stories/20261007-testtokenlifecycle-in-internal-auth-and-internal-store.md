# `TestTokenLifecycle` in `internal/auth` and `internal/store` passes. It fails if a new credential lacks its prefix, a plaintext credential is stored, a legacy token stops working or loses a scope, `POST /tokens` without `scopes` grants `secrets`, an expired or revoked credential is admitted, or an OAuth call's audit row lacks `via`.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestTokenLifecycle` in `internal/auth` and `internal/store` passes. It fails if a new credential lacks its prefix, a plaintext credential is stored, a legacy token stops working or loses a scope, `POST /tokens` without `scopes` grants `secrets`, an expired or revoked credential is admitted, or an OAuth call's audit row lacks `via`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTokenLifecycle` in `internal/auth` and `internal/store` passes. It fails if a new credential lacks its prefix, a plaintext credential is stored, a legacy token stops working or loses a scope, `POST /tokens` without `scopes` grants `secrets`, an expired or revoked credential is admitted, or an OAuth call's audit row lacks `via`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/auth and internal/store with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/auth/enforce_test.go:21 and internal/store/oauth_test.go:18 TestTokenLifecycle asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: prefix -> internal/auth/enforce_test.go:27, internal/api/ai_access_test.go:113; plaintext stored -> enforce_test.go:33, internal/api/ai_access_test.go:117; legacy stops or loses a scope -> internal/store/oauth_test.go:42, internal/api/ai_access_test.go:158; POST /tokens without scopes grants secrets -> internal/api/ai_access_test.go:113,123; expired -> store/oauth_test.go:58, internal/api/ai_access_test.go:139; revoked -> ADDED internal/store/oauth_test.go:60-73 (revoked_at set, refused) and internal/api/ai_access_test.go:140-145 (DELETE, then 401 on the next request); revoked OAuth tokens -> store/oauth_test.go code-reuse, refresh-reuse and RevokeGrant checks; OAuth audit row lacks via -> internal/api/ai_access_test.go:175, store/oauth_test.go:89.
