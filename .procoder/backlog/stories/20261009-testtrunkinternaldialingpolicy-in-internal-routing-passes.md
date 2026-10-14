# `TestTrunkInternalDialingPolicy` in `internal/routing` passes. It fails if any cell of the mode × pattern × DID matrix gives another verdict than the table, if the trace does not name the winning pattern, if mode `off` does not skip the route with a traced reason, or if a feature code becomes reachable.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an administrator, I need each trunk's internal dialing policy (mode and patterns) to decide which internal numbers it may reach, with a traced verdict and no access to feature codes, so that a trunk cannot dial more than I allowed.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkInternalDialingPolicy` in `internal/routing` passes. It fails if any cell of the mode × pattern × DID matrix gives another verdict than the table, if the trace does not name the winning pattern, if mode `off` does not skip the route with a traced reason, or if a feature code becomes reachable.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/routing/engine_test.go:1215 TestTrunkInternalDialingPolicy. main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/routing	1.513s`; passes locally (-v PASS).

