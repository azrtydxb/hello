# `TestScheduler` in `internal/ai` passes against PostgreSQL. It fails if two replicas run one agent at once, a restart re-runs an agent before its interval, run-now does not run once, or a run row lacks its outcome.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-17; plan ai-agent Task 2. Done when the named check passes and fails on each break it lists, so the behaviour of S-17 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestScheduler` in `internal/ai` passes against PostgreSQL. It fails if two replicas run one agent at once, a restart re-runs an agent before its interval, run-now does not run once, or a run row lacks its outcome.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
