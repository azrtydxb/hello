# `TestLabSmoke` in `test/integration` (gated by `HELLO_DOCKER=1`) runs `docker compose -f deploy/docker-compose/compose.yaml up -d --wait` and GETs `localhost:8080/api/v1/version` — fails if any service is unhealthy or the curl is not 200; the README documents exactly these steps.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

A developer must launch a complete two-SIP-node lab with one command (spec §20), following only the README.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestLabSmoke` in `test/integration` (gated by `HELLO_DOCKER=1`) runs `docker compose -f deploy/docker-compose/compose.yaml up -d --wait` and GETs `localhost:8080/api/v1/version` — fails if any service is unhealthy or the curl is not 200; the README documents exactly these steps.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
