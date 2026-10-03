# `TestValkeyFailover` in `test/failure` passes. It fails if, after the Valkey primary is killed, nodes are not READY within 15s of promotion, if new registrations and calls do not succeed, or if any binding lacks a TTL.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestValkeyFailover` in `test/failure` passes. It fails if, after the Valkey primary is killed, nodes are not READY within 15s of promotion, if new registrations and calls do not succeed, or if any binding lacks a TTL.

## Evidence

Fingerprint: sha256:8b1b93c16fb8df92a6a33d40823665d866b0075496a35e6dd97fe16b1a9df431
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestValkeyFailover)$ ./test/integration/

Fingerprint: sha256:1a123fe99a3cf1e7aecf918abd7e182b69f2d2e916493c3bbad072427ce2dc6c
Produced: 72 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestValkeyFailover)$ ./internal/vkconn/
