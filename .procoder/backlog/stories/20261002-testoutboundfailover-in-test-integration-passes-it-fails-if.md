# `TestOutboundFailover` in `test/integration` passes. It fails if a call answered 503 by carrier-primary does not complete through carrier-backup, if a 486 is wrongly failed over, or if the trace lacks either attempt.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestOutboundFailover` in `test/integration` passes. It fails if a call answered 503 by carrier-primary does not complete through carrier-backup, if a 486 is wrongly failed over, or if the trace lacks either attempt.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
