# `TestReadiness` in `internal/ops` passes — fails if `/healthz` is not 200 while a dependency check fails, or `/readyz` is not 503 naming the failing dependency, or not 200 once all checks pass.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Load balancers and Kubernetes need liveness separate from readiness so a node with a down dependency is taken out of rotation, not restarted.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestReadiness` in `internal/ops` passes — fails if `/healthz` is not 200 while a dependency check fails, or `/readyz` is not 503 naming the failing dependency, or not 200 once all checks pass.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
