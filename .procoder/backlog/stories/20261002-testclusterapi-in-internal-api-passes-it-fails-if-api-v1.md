# `TestClusterAPI` in `internal/api` passes. It fails if `/api/v1/cluster` omits a member, state, load, version, revision lag, or dependency health, if a drain request is not audited, or if draining the last READY node is not warned.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestClusterAPI` in `internal/api` passes. It fails if `/api/v1/cluster` omits a member, state, load, version, revision lag, or dependency health, if a drain request is not audited, or if draining the last READY node is not warned.

## Evidence

Fingerprint: sha256:2fffa1560764ff2098c9b6c857e3ec7756494745257a7a4f42a7288b364063cf
Produced: 51 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestClusterAPI|TestClusterMetrics|TestControlNodeLifecycle)$ ./internal/api/
