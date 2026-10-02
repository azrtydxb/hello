# `TestVersionAndOpenAPI` in `internal/api` passes — fails if `/api/v1/version` lacks `version`/`commit`, or `/api/v1/openapi.json` is not OpenAPI 3.x JSON describing that path.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

The UI and integrators need a versioned, documented API surface from the first endpoint (spec §24).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVersionAndOpenAPI` in `internal/api` passes — fails if `/api/v1/version` lacks `version`/`commit`, or `/api/v1/openapi.json` is not OpenAPI 3.x JSON describing that path.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
