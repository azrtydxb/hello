# `TestCallRingAllAndHangup` in `test/integration` passes — fails if a call to an extension with two registered test UAs does not ring both, if the losing UA keeps ringing after the other answers, if SDP differs end to end, or if BYE from either side does not end both legs.

Status: open
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestCallRingAllAndHangup` in `test/integration` passes — fails if a call to an extension with two registered test UAs does not ring both, if the losing UA keeps ringing after the other answers, if SDP differs end to end, or if BYE from either side does not end both legs.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
