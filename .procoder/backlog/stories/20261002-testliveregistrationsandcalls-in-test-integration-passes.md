# `TestLiveRegistrationsAndCalls` in `test/integration` passes — fails if an active call or registration is missing from the API while it exists, or if a call is still listed 30s after its node is killed. Registrations outlive their node by design: they live in Valkey until they expire or the phone re-registers through another node.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLiveRegistrationsAndCalls` in `test/integration` passes — fails if an active call or registration is missing from the API while it exists, or if a call is still listed 30s after its node is killed. Registrations outlive their node by design: they live in Valkey until they expire or the phone re-registers through another node.

## Evidence

Fingerprint: sha256:e7fd8a530465699c5c48f71a40e511470228ebb507986e11e900210a4f0719fa
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestLiveRegistrationsAndCalls$ ./test/integration/
