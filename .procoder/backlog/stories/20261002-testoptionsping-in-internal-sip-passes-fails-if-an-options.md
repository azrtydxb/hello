# `TestOptionsPing` in `internal/sip` passes — fails if an OPTIONS request is not answered 200 with the advertised address in the Via/Contact of the response.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestOptionsPing` in `internal/sip` passes — fails if an OPTIONS request is not answered 200 with the advertised address in the Via/Contact of the response.

## Evidence

Fingerprint: sha256:719527dbf90d6247d6dc27af4f151da42148781e608ed0862d4425982052f36e
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestOptionsPing|TestOptionsViaAdvertised)$ ./internal/sip/
