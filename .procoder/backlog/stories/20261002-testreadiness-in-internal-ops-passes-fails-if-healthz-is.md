# `TestReadiness` in `internal/ops` passes — fails if `/healthz` is not 200 while a dependency check fails, or `/readyz` is not 503 naming the failing dependency, or not 200 once all checks pass.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Load balancers and Kubernetes need liveness separate from readiness so a node with a down dependency is taken out of rotation, not restarted.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestReadiness` in `internal/ops` passes — fails if `/healthz` is not 200 while a dependency check fails, or `/readyz` is not 503 naming the failing dependency, or not 200 once all checks pass.

## Evidence

Fingerprint: sha256:f00072076e71e035559b33dca73d4ce589a74d9c3a060fbec68d773a08e1448c
Produced: 51 bytes, exit 0
Command: go test -race -count=1 -run TestReadiness ./internal/ops/
