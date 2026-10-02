# `TestSnapshotReloadOnNotify` and `TestSnapshotSurvivesDatabaseLoss` in `test/integration` pass — fail if hello-sip does not see a new device within 2s of its creation, or stops authenticating known devices when PostgreSQL is stopped.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestSnapshotReloadOnNotify` and `TestSnapshotSurvivesDatabaseLoss` in `test/integration` pass — fail if hello-sip does not see a new device within 2s of its creation, or stops authenticating known devices when PostgreSQL is stopped.

## Evidence

Fingerprint: sha256:667a0f12b756bb900ba6a87985930e955cb2fd4369fb1e10681b70ae1de8ca4e
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestSnapshotReloadOnNotify$ ./test/integration/

Fingerprint: sha256:a24244b649690fa48ab327b57a8b763a4b9a7177d7ee170125271d78356ba1cc
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestSnapshotSurvivesDatabaseLoss$ ./test/integration/
