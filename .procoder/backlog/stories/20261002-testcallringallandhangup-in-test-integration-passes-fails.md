# `TestCallRingAllAndHangup` in `test/integration` passes — fails if a call to an extension with two registered test UAs does not ring both, if the losing UA keeps ringing after the other answers, if SDP differs end to end, or if BYE from either side does not end both legs.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestCallRingAllAndHangup` in `test/integration` passes — fails if a call to an extension with two registered test UAs does not ring both, if the losing UA keeps ringing after the other answers, if SDP differs end to end, or if BYE from either side does not end both legs.

## Evidence

Fingerprint: sha256:435dd19458f5ddf130323a317152a26d79bcc4ad639052835ed1f69aa72990be
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestCallRingAllAndHangup$ ./test/integration/
