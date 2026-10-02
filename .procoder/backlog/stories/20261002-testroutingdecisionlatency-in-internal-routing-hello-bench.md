# `TestRoutingDecisionLatency` in `internal/routing` (`HELLO_BENCH=1`) reports p99 for 100 routes. It fails if p99 exceeds 1ms.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRoutingDecisionLatency` in `internal/routing` (`HELLO_BENCH=1`) reports p99 for 100 routes. It fails if p99 exceeds 1ms.

## Evidence

Fingerprint: sha256:02a518a271ad6dc0f87bfdfdc55ea5862e63e7116f45348f16c0de13b853ad3e
Produced: 55 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR HELLO_BENCH=1 go test -race -count=1 -run ^(TestRoutingDecisionLatency)$ ./internal/routing/
