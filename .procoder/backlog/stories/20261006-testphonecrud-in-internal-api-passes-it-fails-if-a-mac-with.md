# `TestPhoneCRUD` in `internal/api` passes. It fails if a MAC with separators is not normalised, a duplicate MAC is accepted, a phone without an extension-bound device can be enabled, or the fetch-state fields are writable through the API.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestPhoneCRUD` in `internal/api` passes. It fails if a MAC with separators is not normalised, a duplicate MAC is accepted, a phone without an extension-bound device can be enabled, or the fetch-state fields are writable through the API.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Implemented on branch prov-control (plan Task 3). The test is database-backed (and, for firmware, MinIO-backed) and skips without HELLO_TEST_DATABASE_URL; it runs in the CI go job on the prov-control pull request. Not verified until that job is green.
