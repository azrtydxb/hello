# `TestScheduler` in `internal/ai` passes against PostgreSQL. It fails if two replicas run one agent at once, a restart re-runs an agent before its interval, run-now does not run once, or a run row lacks its outcome.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

A multi-replica cluster needs background agents to run on time, once, never concurrently across replicas, with every run recorded (spec S-17). Done when the scheduler is tested in memory and against PostgreSQL advisory locks.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestScheduler` in `internal/ai` passes against PostgreSQL. It fails if two replicas run one agent at once, a restart re-runs an agent before its interval, run-now does not run once, or a run row lacks its outcome.

## Evidence

- `TestScheduler` (internal/ai/scheduler_test.go:183) now runs the same story in memory and against PostgreSQL (subtest `postgres`, advisory locks on dedicated connections): two replicas never run one agent at once, a restart keeps the interval, run-now runs once, every run row has its outcome, a dead replica's run is closed instance_stopped. `ok internal/ai` in PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`); the PostgreSQL case also ran in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) as TestSchedulerPostgres.
