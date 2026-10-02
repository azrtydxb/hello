# `TestLabSmoke` (`HELLO_DOCKER=1`) also places one outbound and one inbound call through `carrier-primary`. It fails if either does not complete. a new trunk guide under docs/ documents the real-trunk check.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLabSmoke` (`HELLO_DOCKER=1`) also places one outbound and one inbound call through `carrier-primary`. It fails if either does not complete. a new trunk guide under docs/ documents the real-trunk check.

## Evidence

Fingerprint: sha256:6b47b820ca258f07a9f80c001ab267922fbe2ffc0e9483d4f8352ea1fedf4d35
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 20m -run ^(TestLabSmoke)$ ./test/integration/
