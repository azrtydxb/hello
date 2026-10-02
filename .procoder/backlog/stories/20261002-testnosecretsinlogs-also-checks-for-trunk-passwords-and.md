# `TestNoSecretsInLogs` also checks for trunk passwords and `HELLO_SECRET_KEY`. It fails if either appears in any log, trace or CDR.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestNoSecretsInLogs` also checks for trunk passwords and `HELLO_SECRET_KEY`. It fails if either appears in any log, trace or CDR.

## Evidence

Fingerprint: sha256:0d89d699b3ee44b2b2d67dc8efa1972aab99f2c94a9aa6602cd1e372cd16f40b
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestNoSecretsInLogs)$ ./test/integration/

Fingerprint: sha256:251351040477d7cc181b8271b3bc5f67f088a987ac224431348314db85f1c979
Produced: 1347 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestTrunkLogValueRedactsPassword|TestSealOpen)$ ./...
