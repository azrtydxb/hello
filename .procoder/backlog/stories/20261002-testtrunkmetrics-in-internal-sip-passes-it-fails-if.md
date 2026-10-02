# `TestTrunkMetrics` in `internal/sip` passes. It fails if registration, OPTIONS and a completed trunk call do not move the trunk metrics.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkMetrics` in `internal/sip` passes. It fails if registration, OPTIONS and a completed trunk call do not move the trunk metrics.

## Evidence

Fingerprint: sha256:b49bc7f2195b7ed2181eaf1811eeb08f1304ee979349356de1bda9e597aeca7f
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestTrunkMetrics|TestTrunkMetricsPruned)$ ./internal/sip/
