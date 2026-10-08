# TestVoiceAgentRouting in internal/routing passes. It fails if an inbound route or an extension that names a missing, disabled or unconfigured agent compiles, if the decision lacks the agent's sip_user URI, if schedules and trunk filters stop applying, or if the trace omits the agent step.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-10] [S-11] `TestVoiceAgentRouting` in `internal/routing` passes. It fails if an inbound route or an extension that names a missing, disabled or unconfigured agent compiles, if the decision lacks the agent's `sip_user` URI, if schedules and trunk filters stop applying, or if the trace omits the agent step.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
