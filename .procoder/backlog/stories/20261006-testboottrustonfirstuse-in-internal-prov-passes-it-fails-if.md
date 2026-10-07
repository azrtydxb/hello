# `TestBootTrustOnFirstUse` in `internal/prov` passes. It fails if any `/p/boot/` response contains a SIP secret or an admin password; if a token is handed out other than once to an armed, allowlisted phone; if two concurrent first requests for one MAC both get the token; if a request after the hand-off does not get an empty `404`, a `boot_reclaim` record and the `boot reclaimed` flag; if a request from outside `HELLO_PROV_BOOT_CIDRS` disarms the phone; if a phone's first HTTPS fetch does not disarm it; or if re-arm does not rotate the token immediately.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestBootTrustOnFirstUse` in `internal/prov` passes. It fails if any `/p/boot/` response contains a SIP secret or an admin password; if a token is handed out other than once to an armed, allowlisted phone; if two concurrent first requests for one MAC both get the token; if a request after the hand-off does not get an empty `404`, a `boot_reclaim` record and the `boot reclaimed` flag; if a request from outside `HELLO_PROV_BOOT_CIDRS` disarms the phone; if a phone's first HTTPS fetch does not disarm it; or if re-arm does not rotate the token immediately.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:08faf18af60613f9f643e8f18119fe35bfd65a22ec2ede9c32c1099dd17d2616
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestBootTrustOnFirstUse$ ./internal/prov/
