# `TestLabSmoke` in `test/integration` (`HELLO_DOCKER=1`) registers two test UAs through host ports 5060 and 5062 and completes a call between them — fails if either UA cannot register or the call does not complete; the phone-setup guide in docs/ documents the physical-phone steps.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLabSmoke` in `test/integration` (`HELLO_DOCKER=1`) registers two test UAs through host ports 5060 and 5062 and completes a call between them — fails if either UA cannot register or the call does not complete; the phone-setup guide in docs/ documents the physical-phone steps.

## Evidence

Fingerprint: sha256:66af2a8e260c6e3a7a9bf0928b2cbbc53d6a9a30b4fe253e019bff4796455f72
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestLabSmoke$ ./test/integration/
