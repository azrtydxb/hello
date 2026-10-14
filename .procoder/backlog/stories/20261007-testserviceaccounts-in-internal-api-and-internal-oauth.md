# `TestServiceAccounts` in `internal/api` and `internal/oauth` passes. It fails if a secret is returned other than once, a third unrevoked secret is allowed, a client-credentials token gets a scope outside the account's or a refresh token, or a disabled account or revoked secret still works on the next request.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestServiceAccounts` in `internal/api` and `internal/oauth` passes. It fails if a secret is returned other than once, a third unrevoked secret is allowed, a client-credentials token gets a scope outside the account's or a refresh token, or a disabled account or revoked secret still works on the next request.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestServiceAccounts` in `internal/api` and `internal/oauth` passes. It fails if a secret is returned other than once, a third unrevoked secret is allowed, a client-credentials token gets a scope outside the account's or a refresh token, or a disabled account or revoked secret still works on the next request.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/api and internal/oauth with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/api/ai_access_test.go:188 and internal/oauth/oauth_test.go:766 TestServiceAccounts asserts each failure the criterion lists.
