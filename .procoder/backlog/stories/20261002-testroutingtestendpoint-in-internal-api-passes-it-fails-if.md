# `TestRoutingTestEndpoint` in `internal/api` passes. It fails if the route tester's trace differs from the one a real call with the same input produces, or if it writes anything.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRoutingTestEndpoint` in `internal/api` passes. It fails if the route tester's trace differs from the one a real call with the same input produces, or if it writes anything.

## Evidence

Fingerprint: sha256:d00eb4d58169d0e7a415a1c889c90b5db134b447ec97776b4859048c3ab27078
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestRoutingTestEndpoint|TestTesterCapacityAndInboundInputs)$ ./internal/api/
