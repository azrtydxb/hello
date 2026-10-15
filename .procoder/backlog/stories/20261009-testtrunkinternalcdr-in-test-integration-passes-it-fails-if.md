# `TestTrunkInternalCDR` in `internal/sip` passes. It fails if a trunk-to-internal call's CDR lacks direction `inbound`, the resolved extension as `rewritten_destination`, the route name, or the policy and caller-ID steps in the trace.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an operator, I need a trunk-to-internal call's CDR to record the direction, the resolved extension, the route and the policy and caller-ID trace steps, so that I can audit what the trunk reached.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkInternalCDR` in `internal/sip` passes. It fails if a trunk-to-internal call's CDR lacks direction `inbound`, the resolved extension as `rewritten_destination`, the route name, or the policy and caller-ID steps in the trace.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/sip/trunk_internal_test.go:169 TestTrunkInternalCDR asserts direction inbound, rewritten destination 1012, route name, policy and caller-ID trace steps. main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/sip	84.695s`. The story named test/integration; the test lives in internal/sip, criterion corrected.

- Per-condition audit 2026-10-15: direction inbound -> internal/sip/trunk_internal_test.go:179 and :195; resolved extension as rewritten_destination -> :179 (1012) and :195 (+1012 resolved to 1012); route name -> :179 (`Any extension`); policy verdict step -> :183 (`Trunk peer may dial 1012: all internal numbers`) and :199; caller-ID step -> :183 (`as received`) and :199 (`<peer> (trunk name)`). Criterion edit test/integration -> internal/sip only relocates: the CDR is the one hello-sip writes for a real SIP call to a registered phone.
