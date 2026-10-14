# `TestPrune` in `internal/ai` passes. It fails if any table keeps rows past its retention, prunes inside it, or two replicas prune at once.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator of a long-running cluster needs the AI tables to stay bounded and predictable (spec S-26): every table pruned at its retention, nothing pruned early, and only one replica pruning at a time. Done when a test proves each retention boundary and the lock.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestPrune` in `internal/ai` passes. It fails if any table keeps rows past its retention, prunes inside it, or two replicas prune at once.

## Evidence

- `TestPrune` (internal/ai/pg_test.go:150, PostgreSQL via the go job's HELLO_TEST_DATABASE_URL): each AI table gets one row just past and one just inside its retention; prune refuses while another replica holds the `hello:ai:prune` advisory lock, then leaves exactly the inside rows plus open findings/proposals. `ok internal/ai` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
