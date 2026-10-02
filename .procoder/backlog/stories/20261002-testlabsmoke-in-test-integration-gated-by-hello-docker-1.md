# `TestLabSmoke` in `test/integration` (gated by `HELLO_DOCKER=1`) runs `docker compose -f deploy/docker-compose/compose.yaml up -d --wait` and GETs `localhost:8080/api/v1/version` — fails if any service is unhealthy or the curl is not 200; the README documents exactly these steps.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

A developer must launch a complete two-SIP-node lab with one command (spec §20), following only the README.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLabSmoke` in `test/integration` (gated by `HELLO_DOCKER=1`) runs `docker compose -f deploy/docker-compose/compose.yaml up -d --wait` and GETs `localhost:8080/api/v1/version` — fails if any service is unhealthy or the curl is not 200; the README documents exactly these steps.

## Evidence

Fingerprint: sha256:559a048fb77067366a3d02f6abec7763af4081ddc55a7fab98d263074989e39d
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 go test -count=1 -timeout 15m -run TestLabSmoke ./test/integration/
