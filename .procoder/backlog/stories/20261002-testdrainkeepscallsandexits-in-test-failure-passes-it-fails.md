# `TestDrainKeepsCallsAndExits` in `test/failure` passes. It fails if, after a drain request, the draining node accepts a new INVITE, drops the active call, keeps its trunk leases, or exits before that call ends; or if, with `HELLO_DRAIN_TIMEOUT` short, it does not BYE the remaining call and exit.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestDrainKeepsCallsAndExits` in `test/failure` passes. It fails if, after a drain request, the draining node accepts a new INVITE, drops the active call, keeps its trunk leases, or exits before that call ends; or if, with `HELLO_DRAIN_TIMEOUT` short, it does not BYE the remaining call and exit.

## Evidence

Fingerprint: sha256:3723ed726afeda71f011235ce5f320bfffb4f8033e97128430a7bdcd502f5adb
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestDrainKeepsCallsAndExits)$ ./test/integration/

Fingerprint: sha256:c353408928570653082c3eb8aeaea1eb04529aeab18f501d66c9fcaf83c42a6f
Produced: 1628 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestDrainTimeoutDuringAnswer|TestAwaitDrainWithdrawsRequestAfterSIGTERM)$ ./...
