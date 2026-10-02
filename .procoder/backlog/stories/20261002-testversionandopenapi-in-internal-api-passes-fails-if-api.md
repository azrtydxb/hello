# `TestVersionAndOpenAPI` in `internal/api` passes — fails if `/api/v1/version` lacks `version`/`commit`, or `/api/v1/openapi.json` is not OpenAPI 3.x JSON describing that path.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

The UI and integrators need a versioned, documented API surface from the first endpoint (spec §24).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestVersionAndOpenAPI` in `internal/api` passes — fails if `/api/v1/version` lacks `version`/`commit`, or `/api/v1/openapi.json` is not OpenAPI 3.x JSON describing that path.

## Evidence

Fingerprint: sha256:4a4867fe646e50060ec3bc10f88953e90f34d125f31b60a74fb5db58d7327612
Produced: 51 bytes, exit 0
Command: go test -race -count=1 ./internal/api/
