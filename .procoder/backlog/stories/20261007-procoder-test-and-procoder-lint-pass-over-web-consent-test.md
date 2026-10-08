# `procoder test` and `procoder lint` pass over `web/`. `Consent.test.tsx` fails if `secrets` is pre-checked, a non-grantable scope is selectable, the client host is not shown, or deny does not redirect with `access_denied`; `AIAccess.test.tsx` fails if a service-account secret or personal token is shown other than once, revoking a grant does not call the API, or the MCP URL is not the one `GET /api/v1/ai/settings` returns.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

Users approve an MCP client's access on the console's consent screen (client host shown, scopes narrowed, secrets opt-in, bounded by role) and manage connected apps, service accounts, personal tokens and skills on the AI access page.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` and `procoder lint` pass over `web/`. `Consent.test.tsx` fails if `secrets` is pre-checked, a non-grantable scope is selectable, the client host is not shown, or deny does not redirect with `access_denied`; `AIAccess.test.tsx` fails if a service-account secret or personal token is shown other than once, revoking a grant does not call the API, or the MCP URL is not the one `GET /api/v1/ai/settings` returns.

## Evidence

- `pnpm -C web test` 30 files / 219 tests passed, `pnpm -C web typecheck` and `pnpm -C web lint` clean, `procoder lint web` 0 findings (2026-10-08, branch ai-console; API responses mocked until Task 3 lands).
- Consent.test.tsx mutations (snapshot, edit, run, restore, `cmp`): secrets pre-checked, non-grantable scope enabled, redirect not followed (deny), client host replaced by name -> each fails at least one test.
- AIAccess.test.tsx mutations: MCP URL not from /ai/settings, grant revoke without API call, service-account secret dialog not closable, token dialog not closable -> each fails one test.
