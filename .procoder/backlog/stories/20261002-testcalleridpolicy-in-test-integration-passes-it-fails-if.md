# `TestCallerIDPolicy` in `test/integration` passes. It fails if the carrier does not receive the expected caller ID for an extension with an external number, for one without, and with a route transform applied.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestCallerIDPolicy` in `test/integration` passes. It fails if the carrier does not receive the expected caller ID for an extension with an external number, for one without, and with a route transform applied.

## Evidence

Fingerprint: sha256:69cec724f3f7fa949d531938815e2e471d02208271ab45c71f4e7b2ca114e960
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestCallerIDPolicy)$ ./test/integration/
