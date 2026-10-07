# `Users.test.tsx` passes under `procoder test` over `web/`. It fails if a non-admin sees the Users page, a role change does not call `PATCH /api/v1/users/{id}`, or a `viewer` is shown write actions.

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

- [ ] `Users.test.tsx` passes under `procoder test` over `web/`. It fails if a non-admin sees the Users page, a role change does not call `PATCH /api/v1/users/{id}`, or a `viewer` is shown write actions.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
