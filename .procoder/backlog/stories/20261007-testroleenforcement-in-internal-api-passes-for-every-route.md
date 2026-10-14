# `TestRoleEnforcement` in `internal/api` passes. For every route it fails if a role below the route's minimum is admitted or the minimum is refused (with a session, a personal token, an OAuth token and a service account), if a demotion does not apply on the next request, or if a route's role differs from its `x-hello-role`.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestRoleEnforcement` in `internal/api` passes. For every route it fails if a role below the route's minimum is admitted or the minimum is refused (with a session, a personal token, an OAuth token and a service account), if a demotion does not apply on the next request, or if a route's role differs from its `x-hello-role`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRoleEnforcement` in `internal/api` passes. For every route it fails if a role below the route's minimum is admitted or the minimum is refused (with a session, a personal token, an OAuth token and a service account), if a demotion does not apply on the next request, or if a route's role differs from its `x-hello-role`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/api with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/api/users_test.go:86 TestRoleEnforcement asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: below-minimum admitted or minimum refused with a session, personal token, OAuth token and service account on every route -> internal/api/users_test.go:118, plus minted real credentials; demotion on the next request -> internal/api/users_test.go:133,177; route role differs from x-hello-role -> internal/api/users_test.go:196. All asserted; no change.
