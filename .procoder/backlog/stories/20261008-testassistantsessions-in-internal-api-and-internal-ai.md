# `TestAssistantSessions` in `internal/api` and `internal/ai/assistant` passes. It fails if a viewer can post a message, another non-admin user can read a session, a second message during a running task is accepted, more than 20 messages reach the model, an answer cites a tool call that was not made, or a proposal in an answer is stored unvalidated.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestAssistantSessions` in `internal/api` and `internal/ai/assistant` passes. It fails if a viewer can post a message, another non-admin user can read a session, a second message during a running task is accepted, more than 20 messages reach the model, an answer cites a tool call that was not made, or a proposal in an answer is stored unvalidated.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:8aff42bfc050d4f556df7786f79f16bafde66fcbd7a575d3b498286c5f6e3615
Produced: 1489 bytes, exit 0
Command: go test -race -count=1 -run ^TestAssistantSessions$ -v ./internal/ai/assistant/

Assistant half (Task 4): ownership, one task per session, the 20-message window with history as data, stored tool calls, refused citations, validated-only proposals, tools_unsupported and the deleted-user stop all pass. The internal/api half (PostgreSQL) needs HELLO_TEST_DATABASE_URL and runs in CI; open until it is green there.
