# `TestInternalDestination` in `internal/routing` passes. It fails if an `internal` destination does not ring the extension the DID names, if it ignores the normalisation the DID match applied, if a miss does not reject 404 with "not an internal number" in the trace, or if the `extension` destination stops meaning "fixed target".

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

- [ ] `TestInternalDestination` in `internal/routing` passes. It fails if an `internal` destination does not ring the extension the DID names, if it ignores the normalisation the DID match applied, if a miss does not reject 404 with "not an internal number" in the trace, or if the `extension` destination stops meaning "fixed target".

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
