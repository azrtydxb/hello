# phase 1's `TestRoutesMatchOpenAPI`, `TestOpenAPIConformance`, `TestOpenAPIForTools`, `TestRoleEnforcement` and `TestToolsFromOpenAPI` pass with the new operations. `TestAIOperationsMCP` in `internal/mcp` fails if apply, dismiss, acknowledge, chat or run-now is an MCP tool.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-24; plan ai-agent Task 1. Done when the named check passes and fails on each break it lists, so the behaviour of S-24 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] phase 1's `TestRoutesMatchOpenAPI`, `TestOpenAPIConformance`, `TestOpenAPIForTools`, `TestRoleEnforcement` and `TestToolsFromOpenAPI` pass with the new operations. `TestAIOperationsMCP` in `internal/mcp` fails if apply, dismiss, acknowledge, chat or run-now is an MCP tool.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
