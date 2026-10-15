# `TestOpenAPIForTools` in `internal/api` passes. It fails if an operation lacks a summary, a description of at least one sentence, a description on any parameter or top-level body property, `x-hello-scope`, or `x-hello-mcp`, or if a response property carrying a show-once secret lacks `x-hello-secret`.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestOpenAPIForTools` in `internal/api` passes. It fails if an operation lacks a summary, a description of at least one sentence, a description on any parameter or top-level body property, `x-hello-scope`, or `x-hello-mcp`, or if a response property carrying a show-once secret lacks `x-hello-secret`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestOpenAPIForTools` in `internal/api` passes. It fails if an operation lacks a summary, a description of at least one sentence, a description on any parameter or top-level body property, `x-hello-scope`, or `x-hello-mcp`, or if a response property carrying a show-once secret lacks `x-hello-secret`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/api with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/api/openapi_test.go:90 TestOpenAPIForTools asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: summary -> internal/api/openapi_test.go:110; one-sentence description -> :113; parameter description -> :117; top-level body property description -> :124; x-hello-scope -> :133; x-hello-mcp -> :136; credential response property without x-hello-secret -> :160. All asserted; no change.
