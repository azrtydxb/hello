# TestVoiceDisabledWithoutAddress in internal/config and internal/routing passes. It fails if a route to an agent compiles with no HELLO_VOICE_SIP_ADDRESS, or the registry stops editing.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-17] `TestVoiceDisabledWithoutAddress` in `internal/config` and `internal/routing` passes. It fails if a route to an agent compiles with no `HELLO_VOICE_SIP_ADDRESS`, or the registry stops editing.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
