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

- [x] `TestFindingsLifecycle` in `internal/ai/detect` passes. It fails if a candidate creates a second open finding, a candidate absent 30 min is not resolved, a dismissed finding returns within 24 h without a severity rise or stays dismissed after one, the model is called when nothing changed or more than once per interval, or a finding without a model answer is not stored with `explained: false`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `go test -race ./internal/ai/detect/ -run TestFindingsLifecycle` passes: one live row per candidate (and the unique index refuses a second), resolve at 30 min and not at 29, a flapping candidate keeps its finding, dismissal suppresses for 24 h (23.5 h suppressed, 24.5 h new) and a severity rise reopens at once, acknowledge/dismiss audited, model called once for a changed set and not within the interval or when unchanged, failed and budget-exhausted calls leave `explained: false`, validator rejects unknown ids, duplicate ranks and invalid proposals. `go test ./internal/api -run TestAIFindingsAPI` covers the routes. Suppression, resolve and severity-rank mutations killed.
