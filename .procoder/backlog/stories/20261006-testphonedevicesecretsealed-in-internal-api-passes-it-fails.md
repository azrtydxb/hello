# `TestPhoneDeviceSecretSealed` in `internal/api` passes. It fails if binding a device does not rotate and seal its secret and update its HA1 values in one transaction, if the sealed secret is returned by any endpoint, or if unbinding leaves `secret_enc` set.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-2); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestPhoneDeviceSecretSealed` in `internal/api` passes. It fails if binding a device does not rotate and seal its secret and update its HA1 values in one transaction, if the sealed secret is returned by any endpoint, or if unbinding leaves `secret_enc` set.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
