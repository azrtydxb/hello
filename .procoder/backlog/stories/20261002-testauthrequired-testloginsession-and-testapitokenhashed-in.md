# `TestAuthRequired`, `TestLoginSession` and `TestAPITokenHashed` in `internal/api` pass — fail if a protected route answers without a session or token, if the cookie lacks HttpOnly or SameSite=Strict, or if the token's plaintext is found in the database.

Status: open
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestAuthRequired`, `TestLoginSession` and `TestAPITokenHashed` in `internal/api` pass — fail if a protected route answers without a session or token, if the cookie lacks HttpOnly or SameSite=Strict, or if the token's plaintext is found in the database.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
