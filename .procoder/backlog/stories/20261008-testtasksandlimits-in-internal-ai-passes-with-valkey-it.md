# `TestTasksAndLimits` in `internal/ai` passes with Valkey. It fails if a task of a replica whose heartbeat stopped is not failed `instance_stopped` within 15 s, another user reads a task, a third concurrent call starts, an interactive request waits more than 5 s before `429` `ai_busy`, background work runs past 80 % of the budget, or interactive work runs past 100 %.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator needs AI work bounded so it cannot starve the cluster or the budget, and tasks of a dead replica to fail rather than hang (spec S-15, S-16). Done when concurrency, waiting, rate, timeout, budget shares, task ownership and stale-replica handling are tested.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTasksAndLimits` in `internal/ai` passes with Valkey. It fails if a task of a replica whose heartbeat stopped is not failed `instance_stopped` within 15 s, another user reads a task, a third concurrent call starts, an interactive request waits more than 5 s before `429` `ai_busy`, background work runs past 80 % of the budget, or interactive work runs past 100 %.

## Evidence

- `TestTasksAndLimits` (internal/ai/limits_test.go:83): third concurrent call refused, interactive ai_busy after 5 s, background never waits, rate and timeout, background stops at 80 % and interactive at 100 % of the budget, another user cannot read a task, one task per session; with Valkey (CI) a stopped replica's task is failed instance_stopped while a live one's is not; a new subtest pins the production heartbeat (5 s) and stale window (15 s). `ok internal/ai` in PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`) (HELLO_TEST_VALKEY_ADDR set).
