# `TestBootPathServesNoSecrets` in `internal/prov` passes. It fails if any `/p/boot/` response for any vendor, for a known or unknown MAC, contains a SIP secret or a token, or if a per-MAC boot request of an allowlisted phone does not record `boot_pending`.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-10); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md. Depends on the spec's open question 2 (token hand-off for DHCP phones): build the answer-independent part first.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestBootPathServesNoSecrets` in `internal/prov` passes. It fails if any `/p/boot/` response for any vendor, for a known or unknown MAC, contains a SIP secret or a token, or if a per-MAC boot request of an allowlisted phone does not record `boot_pending`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
