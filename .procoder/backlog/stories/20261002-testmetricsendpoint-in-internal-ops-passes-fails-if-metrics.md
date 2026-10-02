# `TestMetricsEndpoint` in `internal/ops` passes — fails if `/metrics` lacks `hello_node_ready` or `hello_build_info`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Operators need Prometheus metrics from day one; node readiness and build info are the base for the cluster view.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestMetricsEndpoint` in `internal/ops` passes — fails if `/metrics` lacks `hello_node_ready` or `hello_build_info`.

## Evidence

Fingerprint: sha256:f912c2e0121762e43bb33c8f7a27f2848e2aa39c93a6797c6bc23f1faf5ae3a9
Produced: 51 bytes, exit 0
Command: go test -race -count=1 -run TestMetricsEndpoint ./internal/ops/
