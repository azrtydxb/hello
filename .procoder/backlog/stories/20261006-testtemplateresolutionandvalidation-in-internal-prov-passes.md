# `TestTemplateResolutionAndValidation` in `internal/prov` passes. It fails if resolution picks other than override, then priority, then specificity, then id; if a template that fails to parse, uses an unknown variable, exceeds 100 ms or 256 KiB is saved; or if a template can reach anything outside the documented variables.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTemplateResolutionAndValidation` in `internal/prov` passes. It fails if resolution picks other than override, then priority, then specificity, then id; if a template that fails to parse, uses an unknown variable, exceeds 100 ms or 256 KiB is saved; or if a template can reach anything outside the documented variables.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:a57d52e848bf92375b14c6adbc4cd6292415acd092d877ab2811e164850c6e0d
Produced: 52 bytes, exit 0
Command: go test -race -count=1 -run ^TestTemplateResolutionAndValidation$ ./internal/prov/
