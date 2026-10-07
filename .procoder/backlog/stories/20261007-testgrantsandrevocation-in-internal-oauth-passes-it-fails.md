# `TestGrantsAndRevocation` in `internal/oauth` passes. It fails if revoking a refresh token or deleting a grant leaves any of its tokens working, a user can see or revoke another user's grant without `admin`, or a revocation writes no audit row.

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

- [ ] `TestGrantsAndRevocation` in `internal/oauth` passes. It fails if revoking a refresh token or deleting a grant leaves any of its tokens working, a user can see or revoke another user's grant without `admin`, or a revocation writes no audit row.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
