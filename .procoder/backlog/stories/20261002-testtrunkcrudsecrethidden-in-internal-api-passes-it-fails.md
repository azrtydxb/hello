# `TestTrunkCRUDSecretHidden` in `internal/api` passes. It fails if a trunk password appears in any response after create, is stored unencrypted, or cannot be decrypted by a holder of `HELLO_SECRET_KEY`.

Status: open
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTrunkCRUDSecretHidden` in `internal/api` passes. It fails if a trunk password appears in any response after create, is stored unencrypted, or cannot be decrypted by a holder of `HELLO_SECRET_KEY`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
