# `TestRollingUpgrade` in `test/failure` passes. It fails if draining and restarting each SIP node in turn, with one long call active, drops that call or makes any new registration or call fail.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRollingUpgrade` in `test/failure` passes. It fails if draining and restarting each SIP node in turn, with one long call active, drops that call or makes any new registration or call fail.

## Evidence

Fingerprint: sha256:01dd9ac8fb2c1add6858cc565cbff278d33982b61fb312d4c1cfd455dac1c2b4
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestRollingUpgrade)$ ./test/integration/

Fingerprint: sha256:ed5263b3ba3edc076baf3d6279191f28efea52d523cdf14cfe293ce85211e7d7
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestDrainTimeoutWaitsForReInvite)$ ./internal/sip/
