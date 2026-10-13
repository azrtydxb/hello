# `TestVoiceDisabledWithoutAddress` in `internal/config` and `internal/routing` passes. It fails if a route to an agent compiles with no `HELLO_VOICE_SIP_ADDRESS`, or the registry stops editing.

Status: cancelled 2026-10-09 — the voice-agent feature left Hello (decision "Pivot: voice agents out of Hello", .procoder/ask/decisions.md)
Created: 2026-10-08
Epic: voice-agents
Sprint: -

> Cancelled with the pivot recorded in `.procoder/ask/decisions.md`: voice agents leave Hello and become a separate product that connects through SIP trunks. History kept.

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceDisabledWithoutAddress` in `internal/config` and `internal/routing` passes. It fails if a route to an agent compiles with no `HELLO_VOICE_SIP_ADDRESS`, or the registry stops editing.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
