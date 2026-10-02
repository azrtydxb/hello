# `TestGracefulShutdown` in `internal/ops` passes — fails if `/readyz` still returns 200 after shutdown begins, or `Serve` does not return nil within the timeout.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Graceful draining (spec §17.7) starts here: on SIGTERM a node must leave rotation before it stops serving.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestGracefulShutdown` in `internal/ops` passes — fails if `/readyz` still returns 200 after shutdown begins, or `Serve` does not return nil within the timeout.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
