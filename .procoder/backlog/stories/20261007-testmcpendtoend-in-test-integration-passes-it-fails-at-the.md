# `TestMCPEndToEnd` in `test/integration` passes. It fails at the first step of S-22 that does not behave as described, including the refusal after revocation.

Status: done 2026-10-08
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

The whole external AI access path is proven in one run: the MCP SDK's own OAuth client discovers Hello, is approved through the API, steps up to write, uses tools and a resource, refreshes, is revoked and refused (spec S-22; plan Task 7).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestMCPEndToEnd` in `test/integration` passes. It fails at the first step of S-22 that does not behave as described, including the refusal after revocation.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:9e0fa9b58132336b11305eeeca3bcac32c731cfbd50b833633b30f6b6d5d6a10
Produced: 120 bytes, exit 0
Command: go test -count=1 -run ^TestMCPEndToEnd$ -v ./test/integration/

Run against a scratch PostgreSQL (HELLO_TEST_DATABASE_URL); also passes in the go job of PR #45. Mutation-checked: skipping the grant revocation fails step 11.

Lab job of PR #45 (run 37717558697, the commit merged as e23b001): --- PASS: TestLabAIAccess and --- PASS: TestNoSecretsInLogs after it, with the service-account secret, its access token and the dynamically registered client's code, verifier and tokens remembered.
