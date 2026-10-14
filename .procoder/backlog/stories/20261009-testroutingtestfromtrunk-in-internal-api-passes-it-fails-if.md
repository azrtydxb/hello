# `TestRoutingTestFromTrunk` in `internal/api` passes. It fails if `"from": "trunk:<id>"` gives a trace that differs from a real call with the same input, or if it writes anything.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an administrator, I need the routing test to simulate a call arriving from a given trunk and trace it as a real call would be, without writing anything, so that I can check trunk policies safely.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestRoutingTestFromTrunk` in `internal/api` passes. It fails if `"from": "trunk:<id>"` gives a trace that differs from a real call with the same input, or if it writes anything.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/api/routingtest_test.go:28 TestRoutingTestFromTrunk (DB-backed via newP2Env; skips locally, runs in CI where HELLO_TEST_DATABASE_URL is set). main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/api	302.708s`.

