# `TestTrunkCRUDSecretHidden` in `internal/api` passes. It fails if a trunk password appears in any response after create, is stored unencrypted, or cannot be decrypted by a holder of `HELLO_SECRET_KEY`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkCRUDSecretHidden` in `internal/api` passes. It fails if a trunk password appears in any response after create, is stored unencrypted, or cannot be decrypted by a holder of `HELLO_SECRET_KEY`.

## Evidence

Fingerprint: sha256:e60c899b06ddf8373b77262c6e84cd2f6125ad1c38a3c721472e530a9667ac2e
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestTrunkCRUDSecretHidden)$ ./internal/api/
