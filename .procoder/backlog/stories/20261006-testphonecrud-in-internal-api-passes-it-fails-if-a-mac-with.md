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

- internal/api/prov_test.go:84 TestPhoneCRUD asserts MAC normalisation, 409 on a duplicate MAC, 400 on enabling an unbound phone, 400 on writing every fetch-state field. Green on main CI run 38615122909, go job (database env set).
- Per-condition audit 2026-10-15: MAC with separators not normalised → internal/api/prov_test.go:89 (and :103-106, any spelling is the same phone); duplicate MAC accepted → internal/api/prov_test.go:103 (409 naming it); unbound phone enabled → internal/api/prov_test.go:159 (after unbinding, :154-158); fetch-state writable → internal/api/prov_test.go:122-143 (gap fixed: the old loop sent "x" to every field, so a type error alone gave the 400 even if a field were writable; now well-typed values for all eleven fetch-state fields plus mac on PATCH and POST, and a GET proving nothing changed).
