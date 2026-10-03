# `TestLabSmoke` (`HELLO_DOCKER=1`) registers phones and completes a call through Kamailio on host UDP 5080, failing otherwise. `docs/ha.md` contains every row of the S-9 table.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLabSmoke` (`HELLO_DOCKER=1`) registers phones and completes a call through Kamailio on host UDP 5080, failing otherwise. `docs/ha.md` contains every row of the S-9 table.

## Evidence

Fingerprint: sha256:a255919c4cbd752c3d2e05eb5942dabf827b2e521f589589d53a6af45cf7d7a7
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestLabSmoke)$ ./test/integration/
