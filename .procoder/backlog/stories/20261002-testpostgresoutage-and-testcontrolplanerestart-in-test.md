# `TestPostgresOutage` and `TestControlPlaneRestart` in `test/failure` pass. They fail if registration or calling stops while PostgreSQL or every hello-control is down, or if CDRs written during the outage are not in PostgreSQL after it returns.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestPostgresOutage` and `TestControlPlaneRestart` in `test/failure` pass. They fail if registration or calling stops while PostgreSQL or every hello-control is down, or if CDRs written during the outage are not in PostgreSQL after it returns.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
