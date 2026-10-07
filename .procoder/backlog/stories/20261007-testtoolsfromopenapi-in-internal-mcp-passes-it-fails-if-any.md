# `TestToolsFromOpenAPI` in `internal/mcp` passes. It fails if any non-excluded operation has no tool or a tool has no operation, a tool's input or output schema differs from the operation's (parameters, required path parameters, body), annotations are wrong for the method, an excluded operation is listed, `tools/list` shows a write tool without `write`, or calling a tool beyond the caller's scope does not get the step-up `403`.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

An agent sees one tool per non-excluded OpenAPI operation, with schemas and annotations taken from the document, filtered by its scopes, and gets the step-up 403 when it calls beyond them (spec S-14; plan Task 5).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestToolsFromOpenAPI` in `internal/mcp` passes. It fails if any non-excluded operation has no tool or a tool has no operation, a tool's input or output schema differs from the operation's (parameters, required path parameters, body), annotations are wrong for the method, an excluded operation is listed, `tools/list` shows a write tool without `write`, or calling a tool beyond the caller's scope does not get the step-up `403`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:ff9082ac7627e2342286ef694deb53ef5e49cfd1cc637b5fce9599a584b68cc2
Produced: 650 bytes, exit 0
Command: go test -race -count=1 -run ^TestToolsFromOpenAPI$ -v ./internal/mcp/

Open: the test passes over fixture operations and iterates every operation of the embedded document, which apispec fills with plan Task 2; re-run after that merge.
