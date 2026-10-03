# `TestPartitionFromValkey` in `test/failure` passes. It fails if a SIP node disconnected from the Valkey network is not UNHEALTHY within 15s, keeps receiving new calls from Kamailio, or does not return to READY within 15s after reconnecting.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestPartitionFromValkey` in `test/failure` passes. It fails if a SIP node disconnected from the Valkey network is not UNHEALTHY within 15s, keeps receiving new calls from Kamailio, or does not return to READY within 15s after reconnecting.

## Evidence

Fingerprint: sha256:55fadcddbeca0f31f1a9848a8f133d2d22700277aa8d8258b7f85ed16ebb8b33
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestPartitionFromValkey)$ ./test/integration/

Fingerprint: sha256:7c164212c9fb586e79a017a3df3098968cab14e4fa15734e95cb0b48efd28352
Produced: 57 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestEffectsInStateOrder)$ ./internal/lifecycle/
