# `TestUserRoles` in `internal/api` and `internal/store` passes. It fails if the migration leaves an existing user other than `admin`, a non-admin can list users or change a role, the last `admin` can be demoted, a role change writes no audit row, `GrantableScopes` gives a `viewer` more than `read` or an `operator` `admin` or `secrets`, or a service account holds a scope outside its role.

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

- [ ] `TestUserRoles` in `internal/api` and `internal/store` passes. It fails if the migration leaves an existing user other than `admin`, a non-admin can list users or change a role, the last `admin` can be demoted, a role change writes no audit row, `GrantableScopes` gives a `viewer` more than `read` or an `operator` `admin` or `secrets`, or a service account holds a scope outside its role.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
