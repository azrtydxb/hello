# `TestDigestMD5AndSHA256`, `TestNonceAcrossNodes` and `TestStaleNonce` in `internal/sip` pass — fail if either algorithm is rejected for a correct password or accepted for a wrong one, if a nonce from another node with the same secret fails, or if an expired nonce is not answered with `stale=true`.

Status: open
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestDigestMD5AndSHA256`, `TestNonceAcrossNodes` and `TestStaleNonce` in `internal/sip` pass — fail if either algorithm is rejected for a correct password or accepted for a wrong one, if a nonce from another node with the same secret fails, or if an expired nonce is not answered with `stale=true`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
