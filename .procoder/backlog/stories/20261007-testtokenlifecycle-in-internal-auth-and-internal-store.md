# `TestTokenLifecycle` in `internal/auth` and `internal/store` passes. It fails if a new credential lacks its prefix, a plaintext credential is stored, a legacy token stops working or loses a scope, `POST /tokens` without `scopes` grants `secrets`, an expired or revoked credential is admitted, or an OAuth call's audit row lacks `via`.

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

- [ ] `TestTokenLifecycle` in `internal/auth` and `internal/store` passes. It fails if a new credential lacks its prefix, a plaintext credential is stored, a legacy token stops working or loses a scope, `POST /tokens` without `scopes` grants `secrets`, an expired or revoked credential is admitted, or an OAuth call's audit row lacks `via`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
