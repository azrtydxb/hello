# `TestProvisioningAsVendors` in `test/integration` (`HELLO_DOCKER=1`) passes. For each first-class vendor it fetches the files over HTTPS in that vendor's request sequence with its User-Agent, parses them, registers a phone with the extracted credentials through Kamailio, and fails if any vendor's sequence is not served, a parsed field differs from the device, or the registration is not answered `200 OK`.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-18); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestProvisioningAsVendors` in `test/integration` (`HELLO_DOCKER=1`) passes. For each first-class vendor it fetches the files over HTTPS in that vendor's request sequence with its User-Agent, parses them, registers a phone with the extracted credentials through Kamailio, and fails if any vendor's sequence is not served, a parsed field differs from the device, or the registration is not answered `200 OK`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
