# `TestAuthRequired`, `TestLoginSession` and `TestAPITokenHashed` in `internal/api` pass — fail if a protected route answers without a session or token, if the cookie lacks HttpOnly or SameSite=Strict, or if the token's plaintext is found in the database.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAuthRequired`, `TestLoginSession` and `TestAPITokenHashed` in `internal/api` pass — fail if a protected route answers without a session or token, if the cookie lacks HttpOnly or SameSite=Strict, or if the token's plaintext is found in the database.

## Evidence

Fingerprint: sha256:9cea84656d4ba9983137dbb0d05737f6f1413bbfe77df5d944d21dfd83caf6d3
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestAuthRequired|TestLoginSession|TestAPITokenHashed)$ ./internal/api/
