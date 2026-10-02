# `TestMigrateIdempotentConcurrent` in `test/integration` (PostgreSQL via `HELLO_TEST_DATABASE_URL`) passes — fails if a second or concurrent `migrate up` errors or applies a migration twice.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

hello-control replicas run migrations at deploy time; two replicas starting together must not corrupt or double-apply the schema.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestMigrateIdempotentConcurrent` in `test/integration` (PostgreSQL via `HELLO_TEST_DATABASE_URL`) passes — fails if a second or concurrent `migrate up` errors or applies a migration twice.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
