# `TestTemplateResolutionAndValidation` in `internal/prov` passes. It fails if resolution picks other than override, then priority, then specificity, then id; if a template that fails to parse, uses an unknown variable, exceeds 100 ms or 256 KiB is saved; or if a template can reach anything outside the documented variables.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-8); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTemplateResolutionAndValidation` in `internal/prov` passes. It fails if resolution picks other than override, then priority, then specificity, then id; if a template that fails to parse, uses an unknown variable, exceeds 100 ms or 256 KiB is saved; or if a template can reach anything outside the documented variables.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
