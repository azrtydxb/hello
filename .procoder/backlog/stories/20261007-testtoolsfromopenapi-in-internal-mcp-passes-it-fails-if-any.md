# `TestToolsFromOpenAPI` in `internal/mcp` passes. It fails if any non-excluded operation has no tool or a tool has no operation, a tool's input or output schema differs from the operation's (parameters, required path parameters, body), annotations are wrong for the method, an excluded operation is listed, `tools/list` shows a write tool without `write`, or calling a tool beyond the caller's scope does not get the step-up `403`.

Status: done 2026-10-08
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

- [x] `TestToolsFromOpenAPI` in `internal/mcp` passes. It fails if any non-excluded operation has no tool or a tool has no operation, a tool's input or output schema differs from the operation's (parameters, required path parameters, body), annotations are wrong for the method, an excluded operation is listed, `tools/list` shows a write tool without `write`, or calling a tool beyond the caller's scope does not get the step-up `403`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:b7036415a421fa0a20386c7057b767578260df6539552f6fa1508a3a9a708b74
Produced: 544 bytes, exit 0
Command: go test -race -count=1 -run ^TestToolsFromOpenAPI$ -v ./internal/mcp/

The embedded_document subtest now runs over the operations plan Task 2 filled (every non-excluded operation has a tool and the reverse); fixture, step-up and excluded subtests pass.
