# phase 1's `TestRoutesMatchOpenAPI`, `TestOpenAPIConformance`, `TestOpenAPIForTools`, `TestRoleEnforcement` and `TestToolsFromOpenAPI` pass with the new operations, and `TestVoiceOperationsMCP` in `internal/mcp` fails if a credential, transcript, runtime or egress operation is an MCP tool or an agent write is a read tool.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] phase 1's `TestRoutesMatchOpenAPI`, `TestOpenAPIConformance`, `TestOpenAPIForTools`, `TestRoleEnforcement` and `TestToolsFromOpenAPI` pass with the new operations, and `TestVoiceOperationsMCP` in `internal/mcp` fails if a credential, transcript, runtime or egress operation is an MCP tool or an agent write is a read tool.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
