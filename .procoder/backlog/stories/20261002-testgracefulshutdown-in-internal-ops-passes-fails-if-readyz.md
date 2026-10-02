# `TestGracefulShutdown` in `internal/ops` passes — fails if `/readyz` still returns 200 after shutdown begins, or `Serve` does not return nil within the timeout.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Graceful draining (spec §17.7) starts here: on SIGTERM a node must leave rotation before it stops serving.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestGracefulShutdown` in `internal/ops` passes — fails if `/readyz` still returns 200 after shutdown begins, or `Serve` does not return nil within the timeout.

## Evidence

Fingerprint: sha256:cd7df262ada789416c82157ca90498e115d78fab86f9f623fb3a59314022c8ec
Produced: 51 bytes, exit 0
Command: go test -race -count=1 -run Shutdown ./internal/ops/
