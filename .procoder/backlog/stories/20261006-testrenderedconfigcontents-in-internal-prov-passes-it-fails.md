# `TestRenderedConfigContents` in `internal/prov` passes. It fails if a built-in rendering for any first-class vendor lacks the server, port, username, auth name, secret, display name, BLF keys, re-check URL with the current token, CA URL, or a pinned firmware URL, or if two renders of the same input differ by a byte.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRenderedConfigContents` in `internal/prov` passes. It fails if a built-in rendering for any first-class vendor lacks the server, port, username, auth name, secret, display name, BLF keys, re-check URL with the current token, CA URL, or a pinned firmware URL, or if two renders of the same input differ by a byte.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:c0a8f3a308883a7ca269bacec5162f2f2f08b964253d5ed1f406ef6d72545ddd
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestRenderedConfigContents$ ./internal/prov/
