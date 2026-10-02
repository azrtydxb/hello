# `TestCallFailureCodes` in `test/integration` passes — fails if an unknown number, an unregistered extension, a busy callee, or a ring timeout produces a code other than 404, 480, 486 or 408 respectively.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestCallFailureCodes` in `test/integration` passes — fails if an unknown number, an unregistered extension, a busy callee, or a ring timeout produces a code other than 404, 480, 486 or 408 respectively.

## Evidence

Fingerprint: sha256:fe5f5318280d608623a3a1164fca5f45acf957f15f4c390f300d92ee146d3d33
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestCallFailureCodes$ ./test/integration/

Fingerprint: sha256:e1a7e2a87292874181e549843c1c22c628ca396e24dfeb0fc96210e9328722df
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestCallFailureCodes)$ ./internal/sip/
