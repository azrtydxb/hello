# `procoder test` and `procoder lint` pass over `web/`. `Phones.test.tsx` fails if the token URL is shown other than once after create, rotate or re-arm, if the admin password is shown without an explicit reveal, the CSV dry-run errors are not shown per row, or the device-binding warning is missing. `ProvTemplates.test.tsx` fails if a server validation error is not shown at its line or the preview shows an unmasked value. `ProvSettings.test.tsx` fails if a redirect credential is shown after saving.

Status: done 2026-10-14
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` and `procoder lint` pass over `web/`. `Phones.test.tsx` fails if the token URL is shown other than once after create, rotate or re-arm, if the admin password is shown without an explicit reveal, the CSV dry-run errors are not shown per row, or the device-binding warning is missing. `ProvTemplates.test.tsx` fails if a server validation error is not shown at its line or the preview shows an unmasked value. `ProvSettings.test.tsx` fails if a redirect credential is shown after saving.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- web/src/pages/Phones.test.tsx:184,254,283 (URL shown once after create, rotate, re-arm), :216 (device-binding warning), :378 (per-row CSV dry-run errors), :481 (admin password only after reveal); ProvTemplates.test.tsx:230,243,294 (errors at their line, masked preview); ProvSettings.test.tsx:64 (no credential after saving). Green on main CI run 38615122909, web job (pnpm lint, pnpm test).
