# `TestProposalDedupe` in `internal/ai/proposal` passes. It fails if an identical open proposal is duplicated, a newer proposal on the same targets does not supersede the older, or a dismissed fingerprint is proposed again within 7 days.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator reviewing proposals needs no duplicates, newer proposals to replace older ones on the same targets, and a dismissed suggestion not to return for a week (spec S-14). Done when dedupe, supersede and suppression are tested.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProposalDedupe` in `internal/ai/proposal` passes. It fails if an identical open proposal is duplicated, a newer proposal on the same targets does not supersede the older, or a dismissed fingerprint is proposed again within 7 days.

## Evidence

- `TestProposalDedupe` (internal/ai/proposal/dedupe_test.go:26, PostgreSQL): identical open proposal refreshed not duplicated, newer proposal on the same target supersedes the older, a dismissed fingerprint suppressed for 7 days and allowed after. `ok internal/ai/proposal` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
