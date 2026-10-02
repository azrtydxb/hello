# `TestDrainKeepsCallsAndExits` in `test/failure` passes. It fails if, after a drain request, the draining node accepts a new INVITE, drops the active call, keeps its trunk leases, or exits before that call ends; or if, with `HELLO_DRAIN_TIMEOUT` short, it does not BYE the remaining call and exit.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestDrainKeepsCallsAndExits` in `test/failure` passes. It fails if, after a drain request, the draining node accepts a new INVITE, drops the active call, keeps its trunk leases, or exits before that call ends; or if, with `HELLO_DRAIN_TIMEOUT` short, it does not BYE the remaining call and exit.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
