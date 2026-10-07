# `TestOAuthThrottle` in `internal/oauth` passes with Valkey and with Valkey stopped. It fails if the eleventh failed client authentication in a minute or the eleventh registration in an hour from one IP is not `429`.

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

- [ ] `TestOAuthThrottle` in `internal/oauth` passes with Valkey and with Valkey stopped. It fails if the eleventh failed client authentication in a minute or the eleventh registration in an hour from one IP is not `429`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
