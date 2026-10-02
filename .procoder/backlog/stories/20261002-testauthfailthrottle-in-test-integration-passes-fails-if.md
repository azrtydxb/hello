# `TestAuthFailThrottle` in `test/integration` passes — fails if the eleventh bad attempt from one IP, sent alternately to both nodes, is challenged instead of rejected with 403.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAuthFailThrottle` in `test/integration` passes — fails if the eleventh bad attempt from one IP, sent alternately to both nodes, is challenged instead of rejected with 403.

## Evidence

Fingerprint: sha256:607c78f4ee5d750757a46d5da19daa2df70b4d4aa7bcee19d9a549e77ce983cb
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestAuthFailThrottle$ ./test/integration/

Fingerprint: sha256:e4dbd99d682bda4dd23a7513bb73a4a0c2b7c050e8d4309bf96be49271c9a06b
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestAuthFailThrottleParallel)$ ./internal/sip/
