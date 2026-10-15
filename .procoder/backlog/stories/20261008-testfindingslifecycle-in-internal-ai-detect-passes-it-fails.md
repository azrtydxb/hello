# `TestFindingsLifecycle` in `internal/ai/detect` passes. It fails if a candidate creates a second open finding, a candidate absent 30 min is not resolved, a dismissed finding returns within 24 h without a severity rise or stays dismissed after one, the model is called when nothing changed or more than once per interval, or a finding without a model answer is not stored with `explained: false`.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator reading findings needs one finding per problem that resolves on its own, respects dismissals, and does not spend model tokens when nothing changed (spec S-18, S-21). Done when the lifecycle and the explain throttling are tested against PostgreSQL.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestFindingsLifecycle` in `internal/ai/detect` passes. It fails if a candidate creates a second open finding, a candidate absent 30 min is not resolved, a dismissed finding returns within 24 h without a severity rise or stays dismissed after one, the model is called when nothing changed or more than once per interval, or a finding without a model answer is not stored with `explained: false`.

## Evidence

- `TestFindingsLifecycle` (internal/ai/detect/findings_test.go:45) passes against PostgreSQL: one live finding per candidate, resolve after 30 min absent, 24 h dismissal unless the severity rises, model called only on change and at most once per interval, unexplained findings stored with explained false. `ok internal/ai/detect` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: every condition asserted by TestFindingsLifecycle (internal/ai/detect): unique live finding, resolve after 30 min not 29, 24 h dismissal and severity-rise reopen, model only on change and once per interval, unexplained stored explained=false; no gap.
