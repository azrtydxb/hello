# `TestInternalDestination` in `internal/routing` passes. It fails if an `internal` destination does not ring the extension the DID names, if it ignores the normalisation the DID match applied, if a miss does not reject 404 with "not an internal number" in the trace, or if the `extension` destination stops meaning "fixed target".

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an administrator, I need an `internal` destination to ring the extension the DID names and to reject misses with 404, so that a trunk can reach internal numbers without a route per extension.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestInternalDestination` in `internal/routing` passes. It fails if an `internal` destination does not ring the extension the DID names, if it ignores the normalisation the DID match applied, if a miss does not reject 404 with "not an internal number" in the trace, or if the `extension` destination stops meaning "fixed target".

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/routing/engine_test.go:1121 TestInternalDestination. main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/routing	1.513s`; passes locally (-v PASS).

