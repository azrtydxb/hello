# `TestRoleEnforcement` in `internal/api` passes. For every route it fails if a role below the route's minimum is admitted or the minimum is refused (with a session, a personal token, an OAuth token and a service account), if a demotion does not apply on the next request, or if a route's role differs from its `x-hello-role`.

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

- [ ] `TestRoleEnforcement` in `internal/api` passes. For every route it fails if a role below the route's minimum is admitted or the minimum is refused (with a session, a personal token, an OAuth token and a service account), if a demotion does not apply on the next request, or if a route's role differs from its `x-hello-role`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
