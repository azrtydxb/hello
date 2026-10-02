# `TestTrunkOptionsHealth` in `test/integration` passes. It fails if stopping a carrier does not mark its destination down within two OPTIONS intervals, if routing still selects the down destination, or if restarting the carrier does not mark it up again.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTrunkOptionsHealth` in `test/integration` passes. It fails if stopping a carrier does not mark its destination down within two OPTIONS intervals, if routing still selects the down destination, or if restarting the carrier does not mark it up again.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
