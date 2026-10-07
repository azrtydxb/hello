# `TestProvEndpointAuth` in `internal/prov` passes. It fails if an unknown token, a MAC mismatch, a disabled or unbound phone, or a per-device request over plain HTTP is served; if any denial answers other than an empty `404`; if a plain-HTTP per-device request does not flag `token_exposed`; or if `If-None-Match` with the current ETag does not answer `304`.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProvEndpointAuth` in `internal/prov` passes. It fails if an unknown token, a MAC mismatch, a disabled or unbound phone, or a per-device request over plain HTTP is served; if any denial answers other than an empty `404`; if a plain-HTTP per-device request does not flag `token_exposed`; or if `If-None-Match` with the current ETag does not answer `304`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:15ab00f8246029cad9a4bb65c2fe308c107e81ca9c090736f106efb25b36d1bc
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestProvEndpointAuth$ ./internal/prov/
