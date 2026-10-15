# `TestAuthorizeAndConsent` in `internal/oauth` passes. It fails if a request without S256 PKCE, with an unregistered or inexact redirect URI, or with a foreign `resource` is accepted; if an error before redirect-URI validation redirects; if a CIMD document that redirects, exceeds 64 KiB, times out, resolves to a private address or names another `client_id` is accepted; if approval issues a scope the user narrowed away or outside `GrantableScopes`; if the redirect lacks `iss` or `state`; or if a bearer token can approve.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestAuthorizeAndConsent` in `internal/oauth` passes. It fails if a request without S256 PKCE, with an unregistered or inexact redirect URI, or with a foreign `resource` is accepted; if an error before redirect-URI validation redirects; if a CIMD document that redirects, exceeds 64 KiB, times out, resolves to a private address or names another `client_id` is accepted; if approval issues a scope the user narrowed away or outside `GrantableScopes`; if the redirect lacks `iss` or `state`; or if a bearer token can approve.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAuthorizeAndConsent` in `internal/oauth` passes. It fails if a request without S256 PKCE, with an unregistered or inexact redirect URI, or with a foreign `resource` is accepted; if an error before redirect-URI validation redirects; if a CIMD document that redirects, exceeds 64 KiB, times out, resolves to a private address or names another `client_id` is accepted; if approval issues a scope the user narrowed away or outside `GrantableScopes`; if the redirect lacks `iss` or `state`; or if a bearer token can approve.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/oauth with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/oauth/oauth_test.go:316 TestAuthorizeAndConsent asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: no/plain S256 PKCE -> internal/oauth/oauth_test.go:396; unregistered or inexact redirect -> internal/oauth/oauth_test.go:348; foreign resource -> internal/oauth/oauth_test.go:396; errors before redirect validation redirect -> internal/oauth/oauth_test.go:348; CIMD document that redirects, exceeds 64 KiB, times out or names another client_id -> internal/oauth/oauth_test.go:355-366 (ADDED: end to end through /oauth/authorize, each refused on the error page for its own logged reason and not saved; before, only the fetcher unit TestCIMDFetch held these); private address -> internal/oauth/oauth_test.go:368-374 (ADDED: guarded dialer, end to end); scope narrowed away -> internal/oauth/oauth_test.go:423,444; outside GrantableScopes -> internal/oauth/oauth_test.go:429; redirect lacks iss or state -> internal/oauth/oauth_test.go:396,437; bearer approves -> internal/oauth/oauth_test.go:417. `go test -race ./internal/oauth/` ok locally.
