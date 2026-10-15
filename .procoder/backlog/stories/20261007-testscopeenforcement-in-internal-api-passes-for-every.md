# `TestScopeEnforcement` in `internal/api` passes. For every operation it fails if a token whose scopes are just below the operation's is admitted, if the operation's own scope (or a higher one) is refused, if the `403` lacks the `insufficient_scope` challenge, if a bearer token can call a `session` operation, or if a session or legacy token is refused anything.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestScopeEnforcement` in `internal/api` passes. For every operation it fails if a token whose scopes are just below the operation's is admitted, if the operation's own scope (or a higher one) is refused, if the `403` lacks the `insufficient_scope` challenge, if a bearer token can call a `session` operation, or if a session or legacy token is refused anything.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestScopeEnforcement` in `internal/api` passes. For every operation it fails if a token whose scopes are just below the operation's is admitted, if the operation's own scope (or a higher one) is refused, if the `403` lacks the `insufficient_scope` challenge, if a bearer token can call a `session` operation, or if a session or legacy token is refused anything.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/api with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/api/scope_test.go:59 TestScopeEnforcement asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: below-scope token admitted -> internal/api/scope_test.go:105; own scope refused -> :113; higher (admin) refused -> :117; 403 lacks the insufficient_scope challenge -> :108; bearer on a session operation -> :113 (exact-session token must be refused) and :121 (legacy); legacy refused a non-session operation -> :121; session refused anything -> TIGHTENED internal/api/scope_test.go:126 (before, a session refused on a session operation went unflagged).
