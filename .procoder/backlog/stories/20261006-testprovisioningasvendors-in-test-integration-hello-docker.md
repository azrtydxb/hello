# `TestProvisioningAsVendors` in `test/integration` (`HELLO_DOCKER=1`) passes. For each first-class vendor it fetches the files over HTTPS in that vendor's request sequence with its User-Agent, parses them, registers a phone with the extracted credentials through Kamailio, and fails if any vendor's sequence is not served, a parsed field differs from the device, or the registration is not answered `200 OK`.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProvisioningAsVendors` in `test/integration` (`HELLO_DOCKER=1`) passes. For each first-class vendor it fetches the files over HTTPS in that vendor's request sequence with its User-Agent, parses them, registers a phone with the extracted credentials through Kamailio, and fails if any vendor's sequence is not served, a parsed field differs from the device, or the registration is not answered `200 OK`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Produced: 0 bytes, exit 0
Command: sh -c gh run view 37626337823 --log | grep -q -- '--- PASS: TestProvisioningAsVendors '
