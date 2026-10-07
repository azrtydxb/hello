# `procoder test` and `procoder lint` pass over `web/`. `Consent.test.tsx` fails if `secrets` is pre-checked, a non-grantable scope is selectable, the client host is not shown, or deny does not redirect with `access_denied`; `AIAccess.test.tsx` fails if a service-account secret or personal token is shown other than once, revoking a grant does not call the API, or the MCP URL is not the one `GET /api/v1/ai/settings` returns.

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

- [ ] `procoder test` and `procoder lint` pass over `web/`. `Consent.test.tsx` fails if `secrets` is pre-checked, a non-grantable scope is selectable, the client host is not shown, or deny does not redirect with `access_denied`; `AIAccess.test.tsx` fails if a service-account secret or personal token is shown other than once, revoking a grant does not call the API, or the MCP URL is not the one `GET /api/v1/ai/settings` returns.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
