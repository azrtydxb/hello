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
- Per-condition audit 2026-10-15 (no gaps): token URL shown other than once after create/rotate/re-arm → web/src/pages/Phones.test.tsx:112-127 (expectShownOnce: present once, gone after Done and after a re-render) called at :200, :251, :279, :299; admin password shown without a reveal → Phones.test.tsx:488-489, :494-499, :518; CSV dry-run errors not per row → Phones.test.tsx:401-408; device-binding warning missing → Phones.test.tsx:234-242; server validation error not at its line → ProvTemplates.test.tsx:127-137 (used at :238, :287) and :275-284; preview unmasked → ProvTemplates.test.tsx:326-341; redirect credential shown after saving → ProvSettings.test.tsx:105-120.
