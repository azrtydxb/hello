# `TestKindRollingUpgrade` in test/kindupgrade, run by the kind-upgrade job in .github/workflows/ci.yaml, passes: fails if the call placed before helm upgrade is lost, cannot be hung up, or leaves a zombie, or (if kind cannot run) the PR states exactly why the job is dispatch-only.

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

- [ ] `TestKindRollingUpgrade` in test/kindupgrade, run by the kind-upgrade job in .github/workflows/ci.yaml, passes: fails if the call placed before helm upgrade is lost, cannot be hung up, or leaves a zombie, or (if kind cannot run) the PR states exactly why the job is dispatch-only.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
