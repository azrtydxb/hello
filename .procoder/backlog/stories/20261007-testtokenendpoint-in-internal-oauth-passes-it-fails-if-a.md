# `TestTokenEndpoint` in `internal/oauth` passes. It fails if a wrong PKCE verifier, a reused or expired code, a mismatched redirect URI or client is accepted; if a reused code does not revoke the grant's tokens; if a refresh token is not rotated, a spent one does not revoke the grant, or a refresh widens scope; if the idle or absolute refresh limit is not enforced; if a token is accepted outside its audience (an MCP-bound token directly on `/api/v1`), or an MCP-bound token is refused inside a replay; or if responses lack `no-store`.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestTokenEndpoint` in `internal/oauth` passes. It fails if a wrong PKCE verifier, a reused or expired code, a mismatched redirect URI or client is accepted; if a reused code does not revoke the grant's tokens; if a refresh token is not rotated, a spent one does not revoke the grant, or a refresh widens scope; if the idle or absolute refresh limit is not enforced; if a token is accepted outside its audience (an MCP-bound token directly on `/api/v1`), or an MCP-bound token is refused inside a replay; or if responses lack `no-store`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTokenEndpoint` in `internal/oauth` passes. It fails if a wrong PKCE verifier, a reused or expired code, a mismatched redirect URI or client is accepted; if a reused code does not revoke the grant's tokens; if a refresh token is not rotated, a spent one does not revoke the grant, or a refresh widens scope; if the idle or absolute refresh limit is not enforced; if a token is accepted outside its audience (an MCP-bound token directly on `/api/v1`), or an MCP-bound token is refused inside a replay; or if responses lack `no-store`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/oauth with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/oauth/oauth_test.go:519 TestTokenEndpoint asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: wrong verifier, client or redirect -> internal/oauth/oauth_test.go:577; reused code -> internal/oauth/oauth_test.go:596 and it revokes the tokens -> internal/oauth/oauth_test.go:599,602; expired code -> internal/oauth/oauth_test.go:615; refresh not rotated -> internal/oauth/oauth_test.go:651; spent refresh does not revoke -> internal/oauth/oauth_test.go:661; refresh widens -> internal/oauth/oauth_test.go:642; idle limit -> internal/oauth/oauth_test.go:676; absolute limit -> internal/oauth/oauth_test.go:687; MCP-bound token on /api/v1 -> internal/oauth/oauth_test.go:626; refused inside its replay -> internal/oauth/oauth_test.go:628; no-store -> internal/oauth/oauth_test.go:577,695 and on each success. All asserted; no change.
