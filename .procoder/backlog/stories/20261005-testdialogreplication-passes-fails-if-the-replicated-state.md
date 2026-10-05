# `TestDialogReplication` passes — fails if the replicated state on Valkey is missing any field needed to re-create a leg (Call-ID, tags, CSeq, routes, contacts, SDP, relay ports, correlation), or the heartbeat does not refresh the TTL.

Status: open
Created: 2026-10-05
Epic: incall-ha
Sprint: -

## Description

Phase 7 deliverable; see .procoder/specs/incall-ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestDialogReplication` passes — fails if the replicated state on Valkey is missing any field needed to re-create a leg (Call-ID, tags, CSeq, routes, contacts, SDP, relay ports, correlation), or the heartbeat does not refresh the TTL.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
