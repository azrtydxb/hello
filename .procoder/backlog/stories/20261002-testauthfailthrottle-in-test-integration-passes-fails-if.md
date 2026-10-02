# `TestAuthFailThrottle` in `test/integration` passes — fails if the eleventh bad attempt from one IP, sent alternately to both nodes, is challenged instead of rejected with 403.

Status: open
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestAuthFailThrottle` in `test/integration` passes — fails if the eleventh bad attempt from one IP, sent alternately to both nodes, is challenged instead of rejected with 403.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
