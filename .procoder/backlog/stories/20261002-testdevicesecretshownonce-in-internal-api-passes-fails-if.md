# `TestDeviceSecretShownOnce` in `internal/api` passes — fails if the secret appears in a GET response, if it is stored instead of HA1 values, or if a duplicate extension number or SIP username is accepted.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestDeviceSecretShownOnce` in `internal/api` passes — fails if the secret appears in a GET response, if it is stored instead of HA1 values, or if a duplicate extension number or SIP username is accepted.

## Evidence

Fingerprint: sha256:a35da3ea9b9af93abc2e0914910751562ee360f2f43ad95600e01965d2e82fe8
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestDeviceSecretShownOnce)$ ./internal/api/
