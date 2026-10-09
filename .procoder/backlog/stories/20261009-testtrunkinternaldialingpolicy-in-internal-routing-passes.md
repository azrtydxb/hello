# `TestTrunkInternalDialingPolicy` in `internal/routing` passes. It fails if any cell of the mode × pattern × DID matrix gives another verdict than the table, if the trace does not name the winning pattern, if mode `off` does not skip the route with a traced reason, or if a feature code becomes reachable.

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

- [ ] `TestTrunkInternalDialingPolicy` in `internal/routing` passes. It fails if any cell of the mode × pattern × DID matrix gives another verdict than the table, if the trace does not name the winning pattern, if mode `off` does not skip the route with a traced reason, or if a feature code becomes reachable.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
