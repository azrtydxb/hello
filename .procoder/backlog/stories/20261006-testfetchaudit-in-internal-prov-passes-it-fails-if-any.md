# `TestFetchAudit` in `internal/prov` passes. It fails if any served or denied request leaves no row with the right result, if a row or a log line contains a token, or if a database failure blocks or fails the response.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestFetchAudit` in `internal/prov` passes. It fails if any served or denied request leaves no row with the right result, if a row or a log line contains a token, or if a database failure blocks or fails the response.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:22cd589d2c5d43854b5bb07eb6d5a3c322c6de9f26cd4ae3e815add415798830
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestFetchAudit$ ./internal/prov/
