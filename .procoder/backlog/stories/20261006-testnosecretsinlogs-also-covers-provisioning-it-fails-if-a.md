# `TestNoSecretsInLogs` also covers provisioning. It fails if a device secret, a token or a redirect credential appears in any log line after the lab test's fetches.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestNoSecretsInLogs` also covers provisioning. It fails if a device secret, a token or a redirect credential appears in any log line after the lab test's fetches.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Produced: 0 bytes, exit 0
Command: sh -c gh run view 37626337823 --log | grep -q -- '--- PASS: TestNoSecretsInLogs '
