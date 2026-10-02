# `TestClusterAPI` in `internal/api` passes. It fails if `/api/v1/cluster` omits a member, state, load, version, revision lag, or dependency health, if a drain request is not audited, or if draining the last READY node is not warned.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestClusterAPI` in `internal/api` passes. It fails if `/api/v1/cluster` omits a member, state, load, version, revision lag, or dependency health, if a drain request is not audited, or if draining the last READY node is not warned.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
