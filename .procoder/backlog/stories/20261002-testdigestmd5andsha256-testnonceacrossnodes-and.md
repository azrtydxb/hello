# `TestDigestMD5AndSHA256`, `TestNonceAcrossNodes` and `TestStaleNonce` in `internal/sip` pass — fail if either algorithm is rejected for a correct password or accepted for a wrong one, if a nonce from another node with the same secret fails, or if an expired nonce is not answered with `stale=true`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestDigestMD5AndSHA256`, `TestNonceAcrossNodes` and `TestStaleNonce` in `internal/sip` pass — fail if either algorithm is rejected for a correct password or accepted for a wrong one, if a nonce from another node with the same secret fails, or if an expired nonce is not answered with `stale=true`.

## Evidence

Fingerprint: sha256:b98adb699def11a67526b9bd7362b5f961ca1ab7709fc4c9feeeb41757346e15
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestDigestMD5AndSHA256|TestNonceAcrossNodes|TestStaleNonce)$ ./internal/sip/
