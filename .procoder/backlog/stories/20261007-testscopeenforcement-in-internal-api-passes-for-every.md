# `TestScopeEnforcement` in `internal/api` passes. For every operation it fails if a token whose scopes are just below the operation's is admitted, if the operation's own scope (or a higher one) is refused, if the `403` lacks the `insufficient_scope` challenge, if a bearer token can call a `session` operation, or if a session or legacy token is refused anything.

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

- [ ] `TestScopeEnforcement` in `internal/api` passes. For every operation it fails if a token whose scopes are just below the operation's is admitted, if the operation's own scope (or a higher one) is refused, if the `403` lacks the `insufficient_scope` challenge, if a bearer token can call a `session` operation, or if a session or legacy token is refused anything.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
