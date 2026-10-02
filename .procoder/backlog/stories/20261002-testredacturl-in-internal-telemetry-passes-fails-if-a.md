# `TestRedactURL` in `internal/telemetry` passes — fails if a database URL's password survives into the logged string.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Secrets must never reach logs (spec §10/§23); the database URL is the first secret every service logs at startup.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestRedactURL` in `internal/telemetry` passes — fails if a database URL's password survives into the logged string.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
