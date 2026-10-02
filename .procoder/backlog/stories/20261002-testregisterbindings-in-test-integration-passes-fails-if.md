# `TestRegisterBindings` in `test/integration` passes — fails if two contacts for one AOR are not both stored, if a refresh through the other node duplicates a binding, or if `Expires: 0` leaves a binding behind.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRegisterBindings` in `test/integration` passes — fails if two contacts for one AOR are not both stored, if a refresh through the other node duplicates a binding, or if `Expires: 0` leaves a binding behind.

## Evidence

Fingerprint: sha256:f37fa99694f3d40ced3381422df35509604ea6ab892cf163c461a7c6b59ce064
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestRegisterBindings$ ./test/integration/
