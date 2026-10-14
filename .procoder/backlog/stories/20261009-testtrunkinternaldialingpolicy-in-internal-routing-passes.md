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

- internal/routing/engine_test.go:1230 TestTrunkInternalDialingPolicy. main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/routing	1.513s`; passes locally (-v PASS).

- Per-condition audit 2026-10-15: matrix cell verdict -> internal/routing/engine_test.go:1259 (patterns: X pattern, exact, +-toggle, X in the DID, out of pattern), :1253 (a non-matching patterns trunk), mode all and off below it, and added :1344 (Go regex class, overlapping patterns in both orders); trace names the winning pattern -> added :1304 (exact trace `Trunk peer may dial 1012: pattern 10XX`) and :1358 (overlap: `pattern 1XXX` vs `pattern 10[12]2` by list order) — before only dialingAllows return values were checked, never the traced verdict of an allowed call; mode off skips with a traced reason -> :1288 (exact trace `Route "Any extension" skipped: internal dialing off on trunk closed`); feature code reachable -> :1247 (*31# denied by patterns) and added :1370 (a 404 reject, never a feature, from a trunk whose pattern names *31#, from mode all and from mode off — the spec says regardless of mode).
