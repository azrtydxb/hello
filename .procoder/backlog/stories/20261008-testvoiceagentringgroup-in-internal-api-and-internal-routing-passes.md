# TestVoiceAgentRingGroup in internal/api and internal/routing passes. It fails if an agent member of ring-all, longest-idle or weighted is accepted, if a member row has both or neither of extension and agent, if the failure target voice_agent does not take the call after the group times out, or if a transfer to an agent extension does not reach it.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-12] [S-13] `TestVoiceAgentRingGroup` in `internal/api` and `internal/routing` passes. It fails if an agent member of `ring-all`, `longest-idle` or `weighted` is accepted, if a member row has both or neither of extension and agent, if the failure target `voice_agent` does not take the call after the group times out, or if a transfer to an agent extension does not reach it.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
