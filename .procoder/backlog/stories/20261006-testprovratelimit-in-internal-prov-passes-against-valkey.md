# `TestProvRateLimit` in `internal/prov` passes against Valkey and with Valkey stopped. It fails if the per-IP, denied-request or per-phone limits are not enforced across two server instances sharing Valkey, if a blocked IP is served within the block window, or if the in-memory fallback is not applied when Valkey is down.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestProvRateLimit` in `internal/prov` passes against Valkey and with Valkey stopped. It fails if the per-IP, denied-request or per-phone limits are not enforced across two server instances sharing Valkey, if a blocked IP is served within the block window, or if the in-memory fallback is not applied when Valkey is down.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
