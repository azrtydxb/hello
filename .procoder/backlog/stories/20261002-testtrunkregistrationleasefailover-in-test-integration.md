# `TestTrunkRegistrationLeaseFailover` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if the simulated carrier does not see exactly one registered contact per trunk, or if, after the registering node is killed, no other node registers within one lease period.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkRegistrationLeaseFailover` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if the simulated carrier does not see exactly one registered contact per trunk, or if, after the registering node is killed, no other node registers within one lease period.

## Evidence

Fingerprint: sha256:9b6f0095c3a420f6426eb72738f245a490e4e71a98816054db330a1b6d075cce
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestTrunkRegistrationLeaseFailover)$ ./test/integration/

Fingerprint: sha256:5a95fe28792da1309405e441e1429f52400d319685b1beec9bbf8adfc18f82a6
Produced: 950 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestTrunkLeaseTakeover|TestTrunkLease)$ ./internal/...
