# `TestPartitionFromValkey` in `test/failure` passes. It fails if a SIP node disconnected from the Valkey network is not UNHEALTHY within 15s, keeps receiving new calls from Kamailio, or does not return to READY within 15s after reconnecting.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestPartitionFromValkey` in `test/failure` passes. It fails if a SIP node disconnected from the Valkey network is not UNHEALTHY within 15s, keeps receiving new calls from Kamailio, or does not return to READY within 15s after reconnecting.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
