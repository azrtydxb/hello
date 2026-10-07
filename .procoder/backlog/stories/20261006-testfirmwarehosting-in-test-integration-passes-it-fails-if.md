# `TestFirmwareHosting` in `test/integration` passes. It fails if an uploaded firmware is not stored with its SHA-256, is served without a valid token or on `/p/boot/`, ignores `Range`, or if a pinned version is missing from the rendered config or deleting a pinned file is allowed.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestFirmwareHosting` in `test/integration` passes. It fails if an uploaded firmware is not stored with its SHA-256, is served without a valid token or on `/p/boot/`, ignores `Range`, or if a pinned version is missing from the rendered config or deleting a pinned file is allowed.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Implemented on branch prov-control (plan Task 3). The test is database-backed (and, for firmware, MinIO-backed) and skips without HELLO_TEST_DATABASE_URL; it runs in the CI go job on the prov-control pull request. Not verified until that job is green.
