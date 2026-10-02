# `TestConfigChangeAuditedAndRevisioned` in `test/integration` passes — fails if creating, updating or deleting an extension does not write an audit row and increase `config_revision` exactly once.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestConfigChangeAuditedAndRevisioned` in `test/integration` passes — fails if creating, updating or deleting an extension does not write an audit row and increase `config_revision` exactly once.

## Evidence

Fingerprint: sha256:d626f8d22bf754e6d50a2280b996b9dfb9c063e0e05a31e6318f5dd0e34ca615
Produced: 55 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestConfigChangeAuditedAndRevisioned)$ ./test/integration/
