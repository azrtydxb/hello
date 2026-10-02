# `TestCDRWritten` in `test/integration` passes — fails if an answered and a cancelled call do not each produce one CDR with correct answer time, billable duration and termination side.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestCDRWritten` in `test/integration` passes — fails if an answered and a cancelled call do not each produce one CDR with correct answer time, billable duration and termination side.

## Evidence

Fingerprint: sha256:6008e58c3f891b28bdc6fdc8b3a4126394f65d244d3ef8bea30cd5ff119aa66f
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestCDRWritten$ ./test/integration/
