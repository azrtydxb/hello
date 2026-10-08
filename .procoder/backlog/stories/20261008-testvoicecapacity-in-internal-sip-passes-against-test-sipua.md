# `TestVoiceCapacity` in `internal/sip` passes against `test/sipua`. It fails if an INVITE is sent after an agent's `max_concurrent` or `HELLO_VOICE_MAX_CALLS` is reached, the busy call does not take the next ring group step, failure target or route failover, the count does not drop when a call ends, or a `486` from the agent is mapped differently from Hello's own busy.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceCapacity` in `internal/sip` passes against `test/sipua`. It fails if an INVITE is sent after an agent's `max_concurrent` or `HELLO_VOICE_MAX_CALLS` is reached, the busy call does not take the next ring group step, failure target or route failover, the count does not drop when a call ends, or a `486` from the agent is mapped differently from Hello's own busy.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
