# `TestCDRTrace` in `test/integration` passes. It fails if an outbound, an inbound and a failed call do not each produce a CDR with direction, original and rewritten destination, route, trunk and a trace whose steps match the call, or if the failed call lacks a failure explanation.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestCDRTrace` in `test/integration` passes. It fails if an outbound, an inbound and a failed call do not each produce a CDR with direction, original and rewritten destination, route, trunk and a trace whose steps match the call, or if the failed call lacks a failure explanation.

## Evidence

Fingerprint: sha256:504d58c0746cf76a7b53c390d5426b62d85d703a46a4461dd048db3f3a78e61b
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestCDRTrace)$ ./test/integration/

Fingerprint: sha256:92160d4916756a842c02e39e3afe550de29d2f8e2cee98abdadc38107cdbc84c
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestCDRDetail)$ ./internal/api/
