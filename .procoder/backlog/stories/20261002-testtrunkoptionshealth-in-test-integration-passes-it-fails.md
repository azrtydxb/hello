# `TestTrunkOptionsHealth` in `test/integration` passes. It fails if stopping a carrier does not mark its destination down within two OPTIONS intervals, if routing still selects the down destination, or if restarting the carrier does not mark it up again.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkOptionsHealth` in `test/integration` passes. It fails if stopping a carrier does not mark its destination down within two OPTIONS intervals, if routing still selects the down destination, or if restarting the carrier does not mark it up again.

## Evidence

Fingerprint: sha256:7dcd352bb56bdafcdb8c7530cba843df206394c12186f340c2e9e4493189db32
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestTrunkOptionsHealth)$ ./test/integration/

Fingerprint: sha256:dbe201599feae00d999d092cdc0845a4e42bc6fe13746512f56ab4e37bf6a380
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestTrunkOptionsHealth)$ ./internal/sip/
