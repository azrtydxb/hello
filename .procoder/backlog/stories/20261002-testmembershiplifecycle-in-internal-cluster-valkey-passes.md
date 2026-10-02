# `TestMembershipLifecycle` in `internal/cluster` (Valkey) passes. It fails if a node is not reported JOINING, then READY, then DRAINING, then OFFLINE with the right timings, if `/readyz` is 200 outside READY, or if an expired node is not tombstoned.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestMembershipLifecycle` in `internal/cluster` (Valkey) passes. It fails if a node is not reported JOINING, then READY, then DRAINING, then OFFLINE with the right timings, if `/readyz` is 200 outside READY, or if an expired node is not tombstoned.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
