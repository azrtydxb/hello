# `TestRouteValidation` in `internal/api` passes. It fails if an uncompilable regex, a template referencing a missing group, a route without trunks, a bad time zone or an oversized regex is accepted.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRouteValidation` in `internal/api` passes. It fails if an uncompilable regex, a template referencing a missing group, a route without trunks, a bad time zone or an oversized regex is accepted.

## Evidence

Fingerprint: sha256:def6bd3cd78ec452e933bed63b33d1306fb07a2431644b379108d045de7237cb
Produced: 52 bytes, exit 0
Command: env HELLO_TEST_DATABASE_URL=$HELLO_TEST_DATABASE_URL HELLO_TEST_VALKEY_ADDR=$HELLO_TEST_VALKEY_ADDR go test -race -count=1 -run ^(TestRouteValidation|TestPreexistingInvalidConfig|TestSourceCIDRBreadth)$ ./internal/api/
