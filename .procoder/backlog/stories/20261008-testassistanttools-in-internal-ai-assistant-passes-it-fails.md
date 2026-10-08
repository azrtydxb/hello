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

- [ ] `TestAssistantTools` in `internal/ai/assistant` passes. It fails if a listed tool is missing from the document, is not `GET`, `read` and MCP-exposed, a tool result exceeds 16 KiB or holds an `x-hello-secret` value, or a message makes more than 8 steps or 16 tool calls.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
