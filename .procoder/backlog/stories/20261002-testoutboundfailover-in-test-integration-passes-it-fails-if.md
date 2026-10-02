# `TestOutboundFailover` in `test/integration` passes. It fails if a call answered 503 by carrier-primary does not complete through carrier-backup, if a 486 is wrongly failed over, or if the trace lacks either attempt.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestOutboundFailover` in `test/integration` passes. It fails if a call answered 503 by carrier-primary does not complete through carrier-backup, if a 486 is wrongly failed over, or if the trace lacks either attempt.

## Evidence

Fingerprint: sha256:54c7525fe9cd09475c5d927f53d363f8a8d1aca68dc3ac70608a771323311eb0
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestOutboundFailover)$ ./test/integration/

Fingerprint: sha256:f8ace4c86396e507a5724f44eb0fa7622340b4d7b18a8af8a451e1385cb2b8ce
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestOutboundFailover503ToBackup|TestOutbound486NotFailedOver)$ ./internal/sip/
