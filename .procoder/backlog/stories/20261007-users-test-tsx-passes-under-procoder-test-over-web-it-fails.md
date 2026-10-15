# `Users.test.tsx` passes under `procoder test` over `web/`. It fails if a non-admin sees the Users page, a role change does not call `PATCH /api/v1/users/{id}`, or a `viewer` is shown write actions.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

As a maintainer of Hello's external AI access (OAuth 2.1, MCP, roles and the OpenAPI contract), I need this guarantee held by an automated test so a regression fails the build instead of reaching an operator. Done means: `Users.test.tsx` passes under `procoder test` over `web/`. It fails if a non-admin sees the Users page, a role change does not call `PATCH /api/v1/users/{id}`, or a `viewer` is shown write actions.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `Users.test.tsx` passes under `procoder test` over `web/`. It fails if a non-admin sees the Users page, a role change does not call `PATCH /api/v1/users/{id}`, or a `viewer` is shown write actions.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- Main CI run 38615122909 (ci.yaml, sha 3861512), web job green: pnpm lint and pnpm test over web/ including web/src/pages/Users.test.tsx; re-run locally 2026-10-15: pnpm test 38 files / 280 tests pass, pnpm lint clean.
- Per-condition audit 2026-10-15: non-admin sees the Users page -> Users.test.tsx 'hides the Users page from a viewer/operator'; a role change does not call PATCH /api/v1/users/{id} -> 'changes a role' (exact call and body); a viewer shown write actions -> 'shows a viewer no write actions'. All asserted; no change.
