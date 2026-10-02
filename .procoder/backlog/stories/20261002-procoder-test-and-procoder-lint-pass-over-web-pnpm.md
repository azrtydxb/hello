# `procoder test` and `procoder lint` pass over `web/` (pnpm typecheck, lint, build, vitest); `Dashboard.test.tsx` fails if the Dashboard does not render the version returned by a mocked `/api/v1/version`.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Administrators need a UI shell with the spec §21 navigation that already talks to the API, so features land into an existing frame.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder test` and `procoder lint` pass over `web/` (pnpm typecheck, lint, build, vitest); `Dashboard.test.tsx` fails if the Dashboard does not render the version returned by a mocked `/api/v1/version`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
