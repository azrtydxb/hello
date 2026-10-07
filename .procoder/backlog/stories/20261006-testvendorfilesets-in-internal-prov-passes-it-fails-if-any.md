# `TestVendorFileSets` in `internal/prov` passes. It fails if any first-class vendor's documented request path (MAC case included) does not resolve to the right file kind, or a path of one vendor resolves for a phone of another.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestVendorFileSets` in `internal/prov` passes. It fails if any first-class vendor's documented request path (MAC case included) does not resolve to the right file kind, or a path of one vendor resolves for a phone of another.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:2dd0da530d49117b32a0421c713e4cc9f548531c80e1ce79f8a8d81711aa0536
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestVendorFileSets$ ./internal/prov/
