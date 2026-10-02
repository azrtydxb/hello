# `TestTrunkRegistrationLeaseFailover` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if the simulated carrier does not see exactly one registered contact per trunk, or if, after the registering node is killed, no other node registers within one lease period.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTrunkRegistrationLeaseFailover` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if the simulated carrier does not see exactly one registered contact per trunk, or if, after the registering node is killed, no other node registers within one lease period.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
