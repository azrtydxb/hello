# `TestInboundRouting` in `test/integration` passes. It fails if an inbound call from a carrier to a DID does not ring the mapped extension, if a call from an IP that isn't a trunk is not rejected with 403, or if a header or schedule condition is ignored.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestInboundRouting` in `test/integration` passes. It fails if an inbound call from a carrier to a DID does not ring the mapped extension, if a call from an IP that isn't a trunk is not rejected with 403, or if a header or schedule condition is ignored.

## Evidence

Fingerprint: sha256:14d20dfcf20236a26136755dfccf3f4285e16c3add9d6c40861fc30305dd5e04
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestInboundRouting)$ ./test/integration/

Fingerprint: sha256:8a80af82c2ff76441d4c8a29a06b154e752104cb83b9ac4cb127eb5b2719ab3f
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestInboundSourceValidation)$ ./internal/sip/
