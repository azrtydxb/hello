# `TestTrunkConcurrencyLimit` in `test/integration` passes. It fails if a trunk with `max_calls` 1 carries a second simultaneous call instead of failing over, or if a counter stays held after its call ends.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTrunkConcurrencyLimit` in `test/integration` passes. It fails if a trunk with `max_calls` 1 carries a second simultaneous call instead of failing over, or if a counter stays held after its call ends.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
