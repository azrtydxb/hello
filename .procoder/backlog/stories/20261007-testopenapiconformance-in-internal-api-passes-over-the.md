# `TestOpenAPIConformance` in `internal/api` passes over the whole package suite. It fails if any response has an undocumented status or a body that does not validate, any request body the suite sends does not validate or reaches an operation without a `requestBody`, or a documented status (outside `401`, `500`, `503` and the commented exemptions) is never observed — including `422` on preview, `502` on each listed MinIO-backed operation, both voicemail PUT body forms and the heard body.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestOpenAPIConformance` in `internal/api` passes over the whole package suite. It fails if any response has an undocumented status or a body that does not validate, any request body the suite sends does not validate or reaches an operation without a `requestBody`, or a documented status (outside `401`, `500`, `503` and the commented exemptions) is never observed — including `422` on preview, `502` on each listed MinIO-backed operation, both voicemail PUT body forms and the heard body.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
