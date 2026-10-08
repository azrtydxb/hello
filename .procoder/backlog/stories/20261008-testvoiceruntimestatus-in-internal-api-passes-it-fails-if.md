# `TestVoiceRuntimeStatus` in `internal/api` passes. It fails if routing, CDRs or registry edits stop while the runtime has not polled (S-22), an ack does not set `last_seen_at`, the revision lag or per-agent state, a seventh ack in a minute is accepted, or status stays green after 2 minutes of silence with an agent routed.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceRuntimeStatus` in `internal/api` passes. It fails if routing, CDRs or registry edits stop while the runtime has not polled (S-22), an ack does not set `last_seen_at`, the revision lag or per-agent state, a seventh ack in a minute is accepted, or status stays green after 2 minutes of silence with an agent routed.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
