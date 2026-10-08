# `TestVoiceCallAuth` in `internal/sip` and `internal/voice` passes. It fails if the signature does not verify with a vector shared with talking-agent (`testdata/voice_auth_vectors.json`), a field change does not break it, the secret appears in a log or the API, or two keys are not both accepted during rotation.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceCallAuth` in `internal/sip` and `internal/voice` passes. It fails if the signature does not verify with a vector shared with talking-agent (`testdata/voice_auth_vectors.json`), a field change does not break it, the secret appears in a log or the API, or two keys are not both accepted during rotation.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
