# `TestKamailioBalancesAndPaths` in `test/failure` passes. It fails if phones registering through Kamailio are not spread over both nodes, if a call between phones registered on different nodes does not connect, or if failed-auth throttling keys on Kamailio's IP instead of the client's.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestKamailioBalancesAndPaths` in `test/failure` passes. It fails if phones registering through Kamailio are not spread over both nodes, if a call between phones registered on different nodes does not connect, or if failed-auth throttling keys on Kamailio's IP instead of the client's.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
