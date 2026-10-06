# `TestPreviewMasksSecrets` in `internal/api` passes. It fails if a preview contains the secret or the token, differs from the served body other than in the masked values, or writes a fetch record.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestPreviewMasksSecrets` in `internal/api` passes. It fails if a preview contains the secret or the token, differs from the served body other than in the masked values, or writes a fetch record.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
