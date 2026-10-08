# `TestKwVoiceAgents` in `test/deploy` passes. It fails if the kw manifests do not set `HELLO_VOICE_SIP_ADDRESS` to the `talking-agent-sip` address, reference Secret `hello-voice` optionally, or commit a secret or token.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestKwVoiceAgents` in `test/deploy` passes. It fails if the kw manifests do not set `HELLO_VOICE_SIP_ADDRESS` to the `talking-agent-sip` address, reference Secret `hello-voice` optionally, or commit a secret or token.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
