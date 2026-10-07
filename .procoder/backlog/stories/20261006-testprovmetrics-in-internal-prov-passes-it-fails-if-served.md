# `TestProvMetrics` in `internal/prov` passes. It fails if served, denied, rate-limited and firmware requests, a redirect operation or a dropped audit row do not move their metrics.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProvMetrics` in `internal/prov` passes. It fails if served, denied, rate-limited and firmware requests, a redirect operation or a dropped audit row do not move their metrics.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:10f2afa026dbb46f905146fab2eda7cca34f20fe136960ff8eb9ef489557ba2c
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestProvMetrics$ ./internal/prov/
