# `TestRedirectLive` in `internal/prov/redirect` (`HELLO_PROV_LIVE_REDIRECT=1`) passes on kw for each of Snom SRAPS, Yealink RPS/YMCS and Grandstream GDMS whose keys exist in the `hello-prov-redirect` secret. For each it registers the live-test device with a test URL, reads it back where the API allows, then restores the previous registration, and fails if any step errors or the read-back URL differs. It fails if a vendor with missing keys is reported as passed instead of skipped with the missing key named. `TestKwProvisioningIngress` also fails if the kw manifest does not mount `hello-prov-redirect` (optional) into hello-control with the S-11 env names.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestRedirectLive` in `internal/prov/redirect` (`HELLO_PROV_LIVE_REDIRECT=1`) passes on kw for each of Snom SRAPS, Yealink RPS/YMCS and Grandstream GDMS whose keys exist in the `hello-prov-redirect` secret. For each it registers the live-test device with a test URL, reads it back where the API allows, then restores the previous registration, and fails if any step errors or the read-back URL differs. It fails if a vendor with missing keys is reported as passed instead of skipped with the missing key named. `TestKwProvisioningIngress` also fails if the kw manifest does not mount `hello-prov-redirect` (optional) into hello-control with the S-11 env names.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
