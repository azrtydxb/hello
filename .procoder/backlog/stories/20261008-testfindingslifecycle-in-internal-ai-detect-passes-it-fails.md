# `TestFindingsLifecycle` in `internal/ai/detect` passes. It fails if a candidate creates a second open finding, a candidate absent 30 min is not resolved, a dismissed finding returns within 24 h without a severity rise or stays dismissed after one, the model is called when nothing changed or more than once per interval, or a finding without a model answer is not stored with `explained: false`.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestFindingsLifecycle` in `internal/ai/detect` passes. It fails if a candidate creates a second open finding, a candidate absent 30 min is not resolved, a dismissed finding returns within 24 h without a severity rise or stays dismissed after one, the model is called when nothing changed or more than once per interval, or a finding without a model answer is not stored with `explained: false`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
