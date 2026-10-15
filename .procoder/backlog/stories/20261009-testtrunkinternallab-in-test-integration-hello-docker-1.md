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

- Per-condition audit 2026-10-15: INVITE to 1012 does not ring 1012 -> test/integration/lab_trunk_internal_test.go:220 (carrier status 200) and :223 (the 1012 phone received the INVITE); audio both ways -> :228 (carrier rtpSent>=10 and rtpReceived>=5) and :232/:235 (the phone received >=5 RTP packets carrying the carrier silence payload 0x55); DID outside the pattern not rejected -> :246 (2012 -> 404); trace lacks the verdict -> :268-275 (`may dial 1012: pattern 10XX`) and :285-291 (`may not dial 2012: no pattern matches`). Criterion edit `1XX` -> `1XX and 10XX` is not a weakening: X is one digit, so `1XX` alone cannot match the four-digit 1012 the spec dials; the spec criterion was self-contradictory.
