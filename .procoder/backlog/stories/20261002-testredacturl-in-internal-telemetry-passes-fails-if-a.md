# `TestRedactURL` in `internal/telemetry` passes — fails if a database URL's password survives into the logged string.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Secrets must never reach logs (spec §10/§23); the database URL is the first secret every service logs at startup.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRedactURL` in `internal/telemetry` passes — fails if a database URL's password survives into the logged string.

## Evidence

Fingerprint: sha256:cc03e906565bf4db0cea7c7242dc4844161e00d140ec891d15812bb751d0238d
Produced: 57 bytes, exit 0
Command: go test -race -count=1 ./internal/telemetry/
