# TestVoiceAgentCRUD in internal/api passes. It fails if a name outside the pattern, a prompt over 8000 characters, an out-of-range limit, a duplicate extension (agent or extension), or an eleventh version is accepted or kept, if sip_user can be edited, if a restore rewrites history instead of adding a revision, or if an audit row holds prompt text.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-1] [S-2] [S-3] [S-4] `TestVoiceAgentCRUD` in `internal/api` passes. It fails if a name outside the pattern, a prompt over 8000 characters, an out-of-range limit, a duplicate extension (agent or extension), or an eleventh version is accepted or kept, if `sip_user` can be edited, if a restore rewrites history instead of adding a revision, or if an audit row holds prompt text.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
