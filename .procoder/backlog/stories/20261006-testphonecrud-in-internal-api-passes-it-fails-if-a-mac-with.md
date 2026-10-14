# `TestPhoneCRUD` in `internal/api` passes. It fails if a MAC with separators is not normalised, a duplicate MAC is accepted, a phone without an extension-bound device can be enabled, or the fetch-state fields are writable through the API.

Status: done 2026-10-14
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestPhoneCRUD` in `internal/api` passes. It fails if a MAC with separators is not normalised, a duplicate MAC is accepted, a phone without an extension-bound device can be enabled, or the fetch-state fields are writable through the API.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/api/prov_test.go:73 TestPhoneCRUD asserts MAC normalisation, 409 on a duplicate MAC, 400 on enabling an unbound phone, 400 on writing every fetch-state field. Green on main CI run 38615122909, go job (database env set).
