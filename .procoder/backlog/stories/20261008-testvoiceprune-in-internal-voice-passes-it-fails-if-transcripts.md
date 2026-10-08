# TestVoicePrune in internal/voice passes. It fails if transcripts outlive their retention, summaries outlive the CDR retention, or two replicas prune at once.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-23] `TestVoicePrune` in `internal/voice` passes. It fails if transcripts outlive their retention, summaries outlive the CDR retention, or two replicas prune at once.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
