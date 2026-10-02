# `TestNoSecretsInLogs` in `test/integration` passes — fails if a device secret, password, API token or Authorization header value appears in any service's log output during the lab call flow.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestNoSecretsInLogs` in `test/integration` passes — fails if a device secret, password, API token or Authorization header value appears in any service's log output during the lab call flow.

## Evidence

Fingerprint: sha256:5611ac8c771a7c058881b0053bba060718cca8443e88d4aa58d76c2bc6fbf9a9
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestNoSecretsInLogs$ ./test/integration/

Fingerprint: sha256:d6119701808d684bc0c5063456e34994d5bde35d623afbb5f98af8afa56ad4e2
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestNoCredentialsInLogs|TestRedactFoldedAndFlow)$ ./internal/sip/
