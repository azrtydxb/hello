# `TestOAuthThrottle` in `internal/oauth` passes with Valkey and with Valkey stopped. It fails if the eleventh failed client authentication in a minute or the eleventh registration in an hour from one IP is not `429`.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestOAuthThrottle` in `internal/oauth` passes with Valkey and with Valkey stopped. It fails if the eleventh failed client authentication in a minute or the eleventh registration in an hour from one IP is not `429`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestOAuthThrottle` in `internal/oauth` passes with Valkey and with Valkey stopped. It fails if the eleventh failed client authentication in a minute or the eleventh registration in an hour from one IP is not `429`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/oauth with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/oauth/oauth_test.go:906 TestOAuthThrottle (in-memory, Valkey down, and Valkey when HELLO_TEST_VALKEY_ADDR is set, as in CI) asserts each failure the criterion lists.
