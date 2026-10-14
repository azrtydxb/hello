# `TestTrunkInternalLab` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if a fakecarrier INVITE to `sip:1012@…` on a trunk with patterns `1XX` and `10XX` (1012 matches `10XX`) does not ring extension 1012 and carry audio both ways, if a DID outside the pattern is not rejected, or if the trace lacks the verdict.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an operator, I need proof in the lab that a carrier INVITE to an internal number inside the trunk's patterns rings the extension with two-way audio and one outside them is rejected, so that the feature works across the real SIP path.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkInternalLab` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if a fakecarrier INVITE to `sip:1012@…` on a trunk with patterns `1XX` and `10XX` (1012 matches `10XX`) does not ring extension 1012 and carry audio both ways, if a DID outside the pattern is not rejected, or if the trace lacks the verdict.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- test/integration/lab_trunk_internal_test.go:174 TestTrunkInternalLab. main CI run 38615122909 lab job (HELLO_DOCKER=1, go test -v ./test/integration/): `=== RUN   TestTrunkInternalLab` then `--- PASS: TestTrunkInternalLab (3.86s)`. The test uses patterns 1XX and 10XX (1012 matches 10XX; 2012 matches none and is rejected 404); criterion corrected.

