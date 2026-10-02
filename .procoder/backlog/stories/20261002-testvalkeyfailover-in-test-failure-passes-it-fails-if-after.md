# `TestValkeyFailover` in `test/failure` passes. It fails if, after the Valkey primary is killed, nodes are not READY within 15s of promotion, if new registrations and calls do not succeed, or if any binding lacks a TTL.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestValkeyFailover` in `test/failure` passes. It fails if, after the Valkey primary is killed, nodes are not READY within 15s of promotion, if new registrations and calls do not succeed, or if any binding lacks a TTL.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
