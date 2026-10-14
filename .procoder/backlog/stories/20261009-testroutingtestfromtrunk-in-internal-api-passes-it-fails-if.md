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

- Per-condition audit 2026-10-15: trace differs from a real call -> internal/api/routingtest_test.go:79, :91, :103, :108 via engineTrace (:127, a direct engine Decide on the saved configuration, the same function hello-sip calls; tester trace must equal it step for step) — added :91 (policy off, 404) and :103/:108 (added trunk b with patterns 1XX: allowed 101 with verdict `pattern 1XX` asserted at :100, and out-of-pattern 1012 -> 404); before only one in-policy call was compared, and a duplicated block was removed; writes anything -> :118 (DB fingerprint of every table + audit count unchanged). DB-backed (newP2Env): skips locally, runs in CI.
