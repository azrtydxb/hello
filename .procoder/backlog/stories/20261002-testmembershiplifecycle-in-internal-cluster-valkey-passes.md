# `TestMembershipLifecycle` in `internal/cluster` (Valkey) passes. It fails if a node is not reported JOINING, then READY, then DRAINING, then OFFLINE with the right timings, if `/readyz` is 200 outside READY, or if an expired node is not tombstoned.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestMembershipLifecycle` in `internal/cluster` (Valkey) passes. It fails if a node is not reported JOINING, then READY, then DRAINING, then OFFLINE with the right timings, if `/readyz` is 200 outside READY, or if an expired node is not tombstoned.

## Evidence

Fingerprint: sha256:43846e2d4c47e2d5b1a6ec39f92d82cf966a4d615a326a7ee0a0dc392d52f214
Produced: 58 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestMembershipLifecycle|TestStateMachine)$ ./internal/lifecycle/
