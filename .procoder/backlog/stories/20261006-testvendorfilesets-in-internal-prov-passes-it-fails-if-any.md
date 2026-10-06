# `TestVendorFileSets` in `internal/prov` passes. It fails if any first-class vendor's documented request path (MAC case included) does not resolve to the right file kind, or a path of one vendor resolves for a phone of another.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-7); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVendorFileSets` in `internal/prov` passes. It fails if any first-class vendor's documented request path (MAC case included) does not resolve to the right file kind, or a path of one vendor resolves for a phone of another.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
