# `TestCDRTrace` in `test/integration` passes. It fails if an outbound, an inbound and a failed call do not each produce a CDR with direction, original and rewritten destination, route, trunk and a trace whose steps match the call, or if the failed call lacks a failure explanation.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestCDRTrace` in `test/integration` passes. It fails if an outbound, an inbound and a failed call do not each produce a CDR with direction, original and rewritten destination, route, trunk and a trace whose steps match the call, or if the failed call lacks a failure explanation.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
