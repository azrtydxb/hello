# `TestRollingUpgrade` in `test/failure` passes. It fails if draining and restarting each SIP node in turn, with one long call active, drops that call or makes any new registration or call fail.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestRollingUpgrade` in `test/failure` passes. It fails if draining and restarting each SIP node in turn, with one long call active, drops that call or makes any new registration or call fail.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
