# `TestSIPMetrics` in `internal/sip` passes — fails if a REGISTER and a completed call do not move `hello_sip_requests_total`, `hello_sip_responses_total`, `hello_calls_total` and `hello_sip_registrations`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestSIPMetrics` in `internal/sip` passes — fails if a REGISTER and a completed call do not move `hello_sip_requests_total`, `hello_sip_responses_total`, `hello_calls_total` and `hello_sip_registrations`.

## Evidence

Fingerprint: sha256:5aa93113eb8f1a658305e6b722955617cf4a0f69c30d58c6e674fd034e018c4d
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestSIPMetrics)$ ./internal/sip/
