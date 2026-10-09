# `TestVoiceCallReport` in `internal/api` and `internal/cdr` passes. It fails if a report is not idempotent, is lost when it arrives before its CDR, stores a transcript without `record_transcript`, exceeds its size limits unrejected, or if `listCDRs` lacks the `voiceAgent` filter or the CDR detail the `voiceAgent` object.

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

- [ ] `TestVoiceCallReport` in `internal/api` and `internal/cdr` passes. It fails if a report is not idempotent, is lost when it arrives before its CDR, stores a transcript without `record_transcript`, exceeds its size limits unrejected, or if `listCDRs` lacks the `voiceAgent` filter or the CDR detail the `voiceAgent` object.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
