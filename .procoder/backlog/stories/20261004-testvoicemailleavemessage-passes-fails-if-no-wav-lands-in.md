# `TestVoicemailLeaveMessage` passes — fails if no WAV lands in MinIO with correct metadata, the greeting/beep order is wrong, `#`/`*` are ignored, MWI counts do not update, or the email (with attachment) is not sent/recorded; `TestVoicemailBusyAndDND` covers busy/DND immediate routing.

Status: open
Created: 2026-10-04
Epic: pbx-features
Sprint: -

## Description

Phase 4 deliverable; see .procoder/specs/pbx-features.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoicemailLeaveMessage` passes — fails if no WAV lands in MinIO with correct metadata, the greeting/beep order is wrong, `#`/`*` are ignored, MWI counts do not update, or the email (with attachment) is not sent/recorded; `TestVoicemailBusyAndDND` covers busy/DND immediate routing.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
