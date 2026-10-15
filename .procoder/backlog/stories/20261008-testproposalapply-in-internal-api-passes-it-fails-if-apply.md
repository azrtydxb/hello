# `TestProposalApply` and `TestProposalApplyHeaders` in `internal/ai/proposal` (driving the `internal/api` handler) pass. It fails if apply replays with anything but the applier's `Cookie`/`Authorization`, a viewer can apply or dismiss, a changed target is applied instead of `stale`, an unrelated revision change makes it stale, a failing action does not stop the sequence with its index and status recorded, a concurrent apply is not `409` `proposal_not_open`, dismiss accepts no reason, or an action's audit row lacks `via` `ai-proposal:<id>`.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator applying an AI proposal needs it applied as them, through the normal API with its audit trail, refused when stale or already handled, and stopped cleanly on a failure (spec S-13). Done when apply and dismiss are tested end to end through the API handler.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProposalApply` and `TestProposalApplyHeaders` in `internal/ai/proposal` (driving the `internal/api` handler) pass. It fails if apply replays with anything but the applier's `Cookie`/`Authorization`, a viewer can apply or dismiss, a changed target is applied instead of `stale`, an unrelated revision change makes it stale, a failing action does not stop the sequence with its index and status recorded, a concurrent apply is not `409` `proposal_not_open`, dismiss accepts no reason, or an action's audit row lacks `via` `ai-proposal:<id>`.

## Evidence

- `TestProposalApply` lives in internal/ai/proposal/apply_test.go:58 and drives the real internal/api handler over HTTP on PostgreSQL (viewer 403 on apply/dismiss, stale on a changed target only, unrelated revision change still applies, failure stops with index/status/applied list, concurrent apply one 200 + one 409, dismiss needs a reason, and now one audit row per action `via ai-proposal:<id>`); `TestProposalApplyHeaders` (internal/ai/proposal/unit_test.go:11) proves only Cookie and Authorization are copied. `ok internal/ai/proposal` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: gaps fixed: the story named internal/api, but the test is TestProposalApply in internal/ai/proposal driving the internal/api handler, and the header half is TestProposalApplyHeaders (text corrected); the concurrent apply asserted only 409, and now requires proposal_not_open. The other conditions were already asserted.
