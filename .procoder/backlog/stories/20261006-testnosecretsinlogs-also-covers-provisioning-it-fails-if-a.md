# `TestNoSecretsInLogs` also covers provisioning. It fails if a device secret, a token or a redirect credential appears in any log line after the lab test's fetches.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestNoSecretsInLogs` also covers provisioning. It fails if a device secret, a token or a redirect credential appears in any log line after the lab test's fetches.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
