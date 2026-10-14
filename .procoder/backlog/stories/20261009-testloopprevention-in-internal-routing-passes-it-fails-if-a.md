# `TestLoopPrevention` in `internal/routing` passes. It fails if a trunk-sourced external route still selects the source trunk without a trace step, if a sole-trunk case answers 503 instead of 403 "loop prevented", or if outbound routing can be re-entered twice on one call.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an operator, I need a trunk-sourced call to be unable to leave through its own trunk or re-enter outbound routing, so that forwarding cannot loop calls between the PBX and a carrier.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLoopPrevention` in `internal/routing` passes. It fails if a trunk-sourced external route still selects the source trunk without a trace step, if a sole-trunk case answers 503 instead of 403 "loop prevented", or if outbound routing can be re-entered twice on one call.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/routing/engine_test.go:1433 TestLoopPrevention (sole trunk -> 403, emptied list -> 403, no outbound re-entry). main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/routing	1.513s`; passes locally (-v PASS).

- Per-condition audit 2026-10-15: source trunk selected without a trace step -> internal/routing/engine_test.go:1481 (Trunks == [other] and exact trace incl. `Trunk solo skipped: call came from it`), :1464 and :1520 also trace the skip; sole-trunk 403 not 503 -> :1464 (Code 403, `Loop prevented: ...`) and :1520 (emptied list with the rest unusable still 403), :1539 (same route from an extension stays 503, so 403 is loop-specific); outbound re-entered twice -> :1559 (hopped source -> 403 `Loop prevented: the call has already been routed outbound once`).
