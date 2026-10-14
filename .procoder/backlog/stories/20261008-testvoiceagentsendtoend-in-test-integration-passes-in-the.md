# `TestVoiceAgentsEndToEnd` in `test/integration` passes in the lab with a fake talking-agent (`test/fakeagent`: a SIP UA that verifies `X-Hello-Auth`, answers, plays a tone, polls the runtime API with a service account, reloads on a revision change within 2 s, and posts a call report). It fails if creating an agent and an inbound route through the API does not make a call from a fake phone reach it, a persona edit does not reach the fake within 2 s, an unsigned or mistimed INVITE is accepted, an agent as ring group failure target does not take the call after the timeout, the CDR lacks the agent and report, or a disabled agent still answers a new call.

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

- [ ] `TestVoiceAgentsEndToEnd` in `test/integration` passes in the lab with a fake talking-agent (`test/fakeagent`: a SIP UA that verifies `X-Hello-Auth`, answers, plays a tone, polls the runtime API with a service account, reloads on a revision change within 2 s, and posts a call report). It fails if creating an agent and an inbound route through the API does not make a call from a fake phone reach it, a persona edit does not reach the fake within 2 s, an unsigned or mistimed INVITE is accepted, an agent as ring group failure target does not take the call after the timeout, the CDR lacks the agent and report, or a disabled agent still answers a new call.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
