# `TestTrunkInternalCDR` in `test/integration` passes. It fails if a trunk-to-internal call's CDR lacks direction `inbound`, the resolved extension as `rewritten_destination`, the route name, or the policy and caller-ID steps in the trace.

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

- [ ] `TestTrunkInternalCDR` in `test/integration` passes. It fails if a trunk-to-internal call's CDR lacks direction `inbound`, the resolved extension as `rewritten_destination`, the route name, or the policy and caller-ID steps in the trace.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
