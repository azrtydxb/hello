# `TestMCPTransport` in `internal/mcp` passes. It fails if `initialize` with `2026-07-28` or `2025-11-25` does not negotiate that version, a session cookie alone is admitted, a foreign `Origin` is not `403`, an unauthenticated request lacks the challenge, or a body over 1 MiB is read.

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

- [ ] `TestMCPTransport` in `internal/mcp` passes. It fails if `initialize` with `2026-07-28` or `2025-11-25` does not negotiate that version, a session cookie alone is admitted, a foreign `Origin` is not `403`, an unauthenticated request lacks the challenge, or a body over 1 MiB is read.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
