# `TestVoiceCDRFields` in `test/integration` passes. It fails if a call routed to an agent writes a CDR without `voice_agent_id` and name, a call that is not routed to one writes them, or deleting the agent erases the name from past CDRs.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceCDRFields` in `test/integration` passes. It fails if a call routed to an agent writes a CDR without `voice_agent_id` and name, a call that is not routed to one writes them, or deleting the agent erases the name from past CDRs.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
