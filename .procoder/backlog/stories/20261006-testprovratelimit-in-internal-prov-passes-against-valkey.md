# `TestProvRateLimit` in `internal/prov` passes against Valkey and with Valkey stopped. It fails if the per-IP, denied-request or per-phone limits are not enforced across two server instances sharing Valkey, if a blocked IP is served within the block window, or if the in-memory fallback is not applied when Valkey is down.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProvRateLimit` in `internal/prov` passes against Valkey and with Valkey stopped. It fails if the per-IP, denied-request or per-phone limits are not enforced across two server instances sharing Valkey, if a blocked IP is served within the block window, or if the in-memory fallback is not applied when Valkey is down.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:1f79f0ff7a9858c013d5dcb05899da64c41f86d58f0b843cd0d89102b52ad378
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestProvRateLimit$ ./internal/prov/
