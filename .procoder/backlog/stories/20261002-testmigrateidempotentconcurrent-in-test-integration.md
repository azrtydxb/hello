# `TestMigrateIdempotentConcurrent` in `test/integration` (PostgreSQL via `HELLO_TEST_DATABASE_URL`) passes — fails if a second or concurrent `migrate up` errors or applies a migration twice.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

hello-control replicas run migrations at deploy time; two replicas starting together must not corrupt or double-apply the schema.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestMigrateIdempotentConcurrent` in `test/integration` (PostgreSQL via `HELLO_TEST_DATABASE_URL`) passes — fails if a second or concurrent `migrate up` errors or applies a migration twice.

## Evidence

Fingerprint: sha256:5892ccaf92a33314b8e634ad9fa977c50241135affe2f0f31531d33425adc69d
Produced: 55 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL go test -race -count=1 -run TestMigrateIdempotentConcurrent ./test/integration/
