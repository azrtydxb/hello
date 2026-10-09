# `TestLoopPrevention` in `internal/routing` passes. It fails if a trunk-sourced external route still selects the source trunk without a trace step, if a sole-trunk case answers 503 instead of 403 "loop prevented", or if outbound routing can be re-entered twice on one call.

Status: open
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestLoopPrevention` in `internal/routing` passes. It fails if a trunk-sourced external route still selects the source trunk without a trace step, if a sole-trunk case answers 503 instead of 403 "loop prevented", or if outbound routing can be re-entered twice on one call.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
