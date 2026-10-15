# `TestInternalCallerIDLadder` in `internal/sip` passes. It fails if a phone sees, for a trunk call with a number, without a number, and without either default, something other than the received caller ID, the trunk's default caller ID, and the bracketed trunk name respectively, or if the route transform is not applied to the produced value.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an extension user receiving a call from a trunk, I need the caller ID I see to follow the ladder (received number, trunk default, bracketed trunk name) with the route transform applied, so that I know who is calling.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestInternalCallerIDLadder` in `internal/sip` passes. It fails if a phone sees, for a trunk call with a number, without a number, and without either default, something other than the received caller ID, the trunk's default caller ID, and the bracketed trunk name respectively, or if the route transform is not applied to the produced value.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/sip/trunk_internal_test.go:93 TestInternalCallerIDLadder (4 cases: received, trunk default, <trunk name>, route transform). internal/sip is not DB-gated; passes locally and in main CI run 38615122909 go job (`go test -race ./...`): `ok  	github.com/azrtydxb/hello/internal/sip	84.695s`. The story named test/integration; the test lives in internal/sip, criterion corrected.

- Per-condition audit 2026-10-15: received caller ID shown -> internal/sip/trunk_internal_test.go:105 (case) asserted at :152 (From user+display name) and :157 (trace); trunk default when none received -> :111/:152/:157; bracketed trunk name when no default -> :118/:152/:157; route transform applied to the produced value -> :124 (received value) and :131 (added: transform on the trunk default, `+97140000001 -> 040000001`)/:152/:157. Criterion edit test/integration -> internal/sip only relocates: the test drives real SIP INVITEs through hello-sip to a registered phone and asserts the From that phone receives.
