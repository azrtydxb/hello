# `TestAssistantTools` in `internal/ai/assistant` passes. It fails if a listed tool is missing from the document, is not `GET`, `read` and MCP-exposed, a tool result exceeds 16 KiB or holds an `x-hello-secret` value, or a message makes more than 8 steps or 16 tool calls.

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

- [x] `TestAssistantTools` in `internal/ai/assistant` passes. It fails if a listed tool is missing from the document, is not `GET`, `read` and MCP-exposed, a tool result exceeds 16 KiB or holds an `x-hello-secret` value, or a message makes more than 8 steps or 16 tool calls.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:f9d9a2d41589d516b2ea73add4921b9dd20b2b515decdb339220a34cd19cf7fa
Produced: 1049 bytes, exit 0
Command: go test -race -count=1 -run ^TestAssistantTools$ -v ./internal/ai/assistant/

All five subtests pass: every listed tool is a GET read MCP tool of the embedded document, the read-only filter refuses POST/HEAD/write/admin/excluded operations, a read replays as the agent with secrets withheld and the result capped at 16 KiB (truncated, total), an oversized object becomes a note, and a looping script is cut at 16 replayed reads and 8 steps per attempt. Mutations of the method, scope and MCP checks and of the call cap are each killed.
