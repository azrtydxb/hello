# `TestFetchAudit` in `internal/prov` passes. It fails if any served or denied request leaves no row with the right result, if a row or a log line contains a token, or if a database failure blocks or fails the response.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestFetchAudit` in `internal/prov` passes. It fails if any served or denied request leaves no row with the right result, if a row or a log line contains a token, or if a database failure blocks or fails the response.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
