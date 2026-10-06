# `TestTokenRollingRotation` in `internal/prov` passes. It fails if the previous token stops working before the phone's first fetch with the new token or the grace period, if a previous-token fetch does not render the new provisioning URL, if `immediate=true` leaves the old token valid, or if only the hash and the sealed token are not what is stored.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTokenRollingRotation` in `internal/prov` passes. It fails if the previous token stops working before the phone's first fetch with the new token or the grace period, if a previous-token fetch does not render the new provisioning URL, if `immediate=true` leaves the old token valid, or if only the hash and the sealed token are not what is stored.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
