# `procoder test` and `procoder lint` pass over `web/`. `Phones.test.tsx` fails if the token URL is shown other than once after create, rotate or re-arm, if the admin password is shown without an explicit reveal, the CSV dry-run errors are not shown per row, or the device-binding warning is missing. `ProvTemplates.test.tsx` fails if a server validation error is not shown at its line or the preview shows an unmasked value. `ProvSettings.test.tsx` fails if a redirect credential is shown after saving.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder test` and `procoder lint` pass over `web/`. `Phones.test.tsx` fails if the token URL is shown other than once after create, rotate or re-arm, if the admin password is shown without an explicit reveal, the CSV dry-run errors are not shown per row, or the device-binding warning is missing. `ProvTemplates.test.tsx` fails if a server validation error is not shown at its line or the preview shows an unmasked value. `ProvSettings.test.tsx` fails if a redirect credential is shown after saving.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
