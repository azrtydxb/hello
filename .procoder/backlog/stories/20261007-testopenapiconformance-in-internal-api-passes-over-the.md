# `TestOpenAPIConformance` in `internal/api` passes over the whole package suite. It fails if any response has an undocumented status or a body that does not validate, any request body the suite sends does not validate or reaches an operation without a `requestBody`, or a documented status (outside `401`, `500`, `503` and the commented exemptions) is never observed — including `422` on preview, `502` on each listed MinIO-backed operation, both voicemail PUT body forms and the heard body.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `TestOpenAPIConformance` in `internal/api` passes over the whole package suite. It fails if any response has an undocumented status or a body that does not validate, any request body the suite sends does not validate or reaches an operation without a `requestBody`, or a documented status (outside `401`, `500`, `503` and the commented exemptions) is never observed — including `422` on preview, `502` on each listed MinIO-backed operation, both voicemail PUT body forms and the heard body.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestOpenAPIConformance` in `internal/api` passes over the whole package suite. It fails if any response has an undocumented status or a body that does not validate, any request body the suite sends does not validate or reaches an operation without a `requestBody`, or a documented status (outside `401`, `500`, `503` and the commented exemptions) is never observed — including `422` on preview, `502` on each listed MinIO-backed operation, both voicemail PUT body forms and the heard body.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/api with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/api/conformance_test.go:95 TestOpenAPIConformance (run from TestMain after the whole suite) asserts each failure the criterion lists.
