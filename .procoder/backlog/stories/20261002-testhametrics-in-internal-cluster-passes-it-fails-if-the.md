# `TestHAMetrics` in `internal/cluster` passes. It fails if the state, member, revision-lag and drain metrics do not move through a JOINING → READY → DRAINING cycle.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestHAMetrics` in `internal/cluster` passes. It fails if the state, member, revision-lag and drain metrics do not move through a JOINING → READY → DRAINING cycle.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
