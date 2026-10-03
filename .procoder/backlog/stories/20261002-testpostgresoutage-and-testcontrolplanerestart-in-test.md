# `TestPostgresOutage` and `TestControlPlaneRestart` in `test/failure` pass. They fail if registration or calling stops while PostgreSQL or every hello-control is down, or if CDRs written during the outage are not in PostgreSQL after it returns.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestPostgresOutage` and `TestControlPlaneRestart` in `test/failure` pass. They fail if registration or calling stops while PostgreSQL or every hello-control is down, or if CDRs written during the outage are not in PostgreSQL after it returns.

## Evidence

Fingerprint: sha256:8b58963af46de07aa1494299ba4d58c27fd7918b0abc33608082320e7e6dca96
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestPostgresOutage|TestControlPlaneRestart)$ ./test/integration/
