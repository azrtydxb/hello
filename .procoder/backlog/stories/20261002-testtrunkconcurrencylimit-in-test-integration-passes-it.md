# `TestTrunkConcurrencyLimit` in `test/integration` passes. It fails if a trunk with `max_calls` 1 carries a second simultaneous call instead of failing over, or if a counter stays held after its call ends.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkConcurrencyLimit` in `test/integration` passes. It fails if a trunk with `max_calls` 1 carries a second simultaneous call instead of failing over, or if a counter stays held after its call ends.

## Evidence

Fingerprint: sha256:dee331e2baf9376ce159f57d13bb1f97f2ef58963d254f42f496859fa072bb01
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestTrunkConcurrencyLimit)$ ./test/integration/

Fingerprint: sha256:27d03653e08ad143a081f4db3bd2f0cffcfe73bc44e87551309d41f1ab761f63
Produced: 57 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestTrunkSlotSurvivesRefreshPastTTL|TestTrunkSlotReacquiredAfterLoss|TestTrunkCallSlots)$ ./internal/livestate/
