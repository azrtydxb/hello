# `TestKamailioBalancesAndPaths` in `test/failure` passes. It fails if phones registering through Kamailio are not spread over both nodes, if a call between phones registered on different nodes does not connect, or if failed-auth throttling keys on Kamailio's IP instead of the client's.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKamailioBalancesAndPaths` in `test/failure` passes. It fails if phones registering through Kamailio are not spread over both nodes, if a call between phones registered on different nodes does not connect, or if failed-auth throttling keys on Kamailio's IP instead of the client's.

## Evidence

Fingerprint: sha256:20193711fc4a2c760d287a87e6ee24625d3f06acace3dba379d6d22916c96f67
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestKamailioBalancesAndPaths)$ ./test/integration/
