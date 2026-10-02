# `TestMetricsEndpoint` in `internal/ops` passes — fails if `/metrics` lacks `hello_node_ready` or `hello_build_info`.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Operators need Prometheus metrics from day one; node readiness and build info are the base for the cluster view.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestMetricsEndpoint` in `internal/ops` passes — fails if `/metrics` lacks `hello_node_ready` or `hello_build_info`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
