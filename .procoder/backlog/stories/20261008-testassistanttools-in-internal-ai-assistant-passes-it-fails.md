# `TestAssistantTools` in `internal/ai/assistant` passes. It fails if a listed tool is missing from the document, is not `GET`, `read` and MCP-exposed, a tool result exceeds 16 KiB or holds an `x-hello-secret` value, or a message makes more than 8 steps or 16 tool calls.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator asking the assistant needs it to read only what they could read themselves, through documented read-only API operations, without leaking secrets or looping without end (spec S-6). Done when the tool list, the result caps and the step and call limits are enforced by test.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAssistantTools` in `internal/ai/assistant` passes. It fails if a listed tool is missing from the document, is not `GET`, `read` and MCP-exposed, a tool result exceeds 16 KiB or holds an `x-hello-secret` value, or a message makes more than 8 steps or 16 tool calls.

## Evidence

- `TestAssistantTools` (internal/ai/assistant/tools_test.go:23): `ok internal/ai/assistant` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`). Subtests check every listed tool is a GET, read-scoped, MCP-exposed operation of the document; the read-only filter refuses writes and exclusions; results withhold x-hello-secret values and are capped at 16 KiB; a message stops at 8 steps (DefaultMaxSteps) and 16 tool calls (MaxToolCalls).
