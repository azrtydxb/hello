# `TestHAMetrics` in `internal/cluster` passes. It fails if the state, member, revision-lag and drain metrics do not move through a JOINING → READY → DRAINING cycle.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestHAMetrics` in `internal/cluster` passes. It fails if the state, member, revision-lag and drain metrics do not move through a JOINING → READY → DRAINING cycle.

## Evidence

Fingerprint: sha256:ae64524b3678aef202fc7cf535adb2b8b80a3a13d22cfa7aa21b5351b3c9cf2e
Produced: 1628 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestHAMetrics|TestClusterMetrics)$ ./...
