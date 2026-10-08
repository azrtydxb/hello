# `procoder test` and `procoder lint` over `web/` pass. `VoiceAgents.test.tsx`, `VoiceAgentEdit.test.tsx`, `VoiceMCPServers.test.tsx` and `VoiceRouting.test.tsx` fail if a viewer sees an edit control, an operator sees a credential field, a credential value is rendered, discovery output is rendered as HTML, an empty allowlist hides its warning, the routing pickers omit the voice agent, a disabled agent is selectable, or the Test tab does not follow the next CDR.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder test` and `procoder lint` over `web/` pass. `VoiceAgents.test.tsx`, `VoiceAgentEdit.test.tsx`, `VoiceMCPServers.test.tsx` and `VoiceRouting.test.tsx` fail if a viewer sees an edit control, an operator sees a credential field, a credential value is rendered, discovery output is rendered as HTML, an empty allowlist hides its warning, the routing pickers omit the voice agent, a disabled agent is selectable, or the Test tab does not follow the next CDR.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
