# `TestInboundRouting` in `test/integration` passes. It fails if an inbound call from a carrier to a DID does not ring the mapped extension, if a call from an IP that isn't a trunk is not rejected with 403, or if a header or schedule condition is ignored.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestInboundRouting` in `test/integration` passes. It fails if an inbound call from a carrier to a DID does not ring the mapped extension, if a call from an IP that isn't a trunk is not rejected with 403, or if a header or schedule condition is ignored.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
