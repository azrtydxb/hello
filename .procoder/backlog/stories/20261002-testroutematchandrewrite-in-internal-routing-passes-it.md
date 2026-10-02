# `TestRouteMatchAndRewrite` in `internal/routing` passes. It fails if prefix, regex, source-extension or schedule matching, or the strip/prefix/template transforms, give a result other than the table of cases, or if the trace omits any step.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRouteMatchAndRewrite` in `internal/routing` passes. It fails if prefix, regex, source-extension or schedule matching, or the strip/prefix/template transforms, give a result other than the table of cases, or if the trace omits any step.

## Evidence

Fingerprint: sha256:cefb3f4d3b6f8f51a675b319cbbf56b3b4428cf574423c5489c4a05cfb8d2f90
Produced: 55 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestRouteMatchAndRewrite|TestSchedule|TestCompileValidation)$ ./internal/routing/
