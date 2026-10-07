# `TestTokenRollingRotation` in `internal/prov` passes. It fails if the previous token stops working before the phone's first fetch with the new token or the grace period, if a previous-token fetch does not render the new provisioning URL, if `immediate=true` leaves the old token valid, or if only the hash and the sealed token are not what is stored.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTokenRollingRotation` in `internal/prov` passes. It fails if the previous token stops working before the phone's first fetch with the new token or the grace period, if a previous-token fetch does not render the new provisioning URL, if `immediate=true` leaves the old token valid, or if only the hash and the sealed token are not what is stored.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:17ee6172a0360e4572fe66a4b1ad221e985c1f9af818e94f0435469acf0c48e8
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestTokenRollingRotation$ ./internal/prov/
