# `TestVoiceAgentInvite` in `internal/sip` passes against `test/sipua` as a fake voice agent. It fails if hello-sip accepts an INVITE or REGISTER that originates from the voice agent's address as a caller (S-14), the INVITE lacks any header of S-15, copies one from the caller, offers a codec other than PCMU/PCMA, or if `404`, `403`, `486`, `503` and silence are not each mapped to the failure path with the right reason.

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

- [ ] `TestVoiceAgentInvite` in `internal/sip` passes against `test/sipua` as a fake voice agent. It fails if hello-sip accepts an INVITE or REGISTER that originates from the voice agent's address as a caller (S-14), the INVITE lacks any header of S-15, copies one from the caller, offers a codec other than PCMU/PCMA, or if `404`, `403`, `486`, `503` and silence are not each mapped to the failure path with the right reason.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
