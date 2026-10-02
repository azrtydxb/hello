# `procoder test` and `procoder lint` pass over `web/`. `Cluster.test.tsx` fails if a node's state, load, version or revision lag is not shown, or if draining the last READY node doesn't ask for confirmation.

Status: open
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder test` and `procoder lint` pass over `web/`. `Cluster.test.tsx` fails if a node's state, load, version or revision lag is not shown, or if draining the last READY node doesn't ask for confirmation.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
