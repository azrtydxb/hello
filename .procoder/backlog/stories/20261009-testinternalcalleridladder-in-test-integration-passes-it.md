# `TestInternalCallerIDLadder` in `test/integration` passes. It fails if a phone sees, for a trunk call with a number, without a number, and without either default, something other than the received caller ID, the trunk's default caller ID, and the bracketed trunk name respectively, or if the route transform is not applied to the produced value.

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

- [ ] `TestInternalCallerIDLadder` in `test/integration` passes. It fails if a phone sees, for a trunk call with a number, without a number, and without either default, something other than the received caller ID, the trunk's default caller ID, and the bracketed trunk name respectively, or if the route transform is not applied to the produced value.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
