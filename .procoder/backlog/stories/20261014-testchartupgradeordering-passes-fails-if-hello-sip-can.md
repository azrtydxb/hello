# `TestChartUpgradeOrdering` passes: fails if hello-sip can update more than one node at a time, its grace period does not exceed the drain timeout, or Kamailio lacks Recreate and the wait-dispatcher init container under hostNetwork.

Status: open
Created: 2026-10-14
Epic: kubernetes
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestChartUpgradeOrdering` passes: fails if hello-sip can update more than one node at a time, its grace period does not exceed the drain timeout, or Kamailio lacks Recreate and the wait-dispatcher init container under hostNetwork.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
