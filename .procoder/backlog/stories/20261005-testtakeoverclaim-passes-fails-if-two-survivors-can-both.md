# `TestTakeoverClaim` passes — fails if two survivors can both claim the same orphaned dialog, or a claim is not released when the original owner reappears pre-takeover.

Status: done 2026-10-06
Created: 2026-10-05
Epic: incall-ha
Sprint: -

## Description

Phase 7 deliverable; see .procoder/specs/incall-ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTakeoverClaim` passes — fails if two survivors can both claim the same orphaned dialog, or a claim is not released when the original owner reappears pre-takeover.

## Evidence

Fingerprint: sha256:kw-live-proof-2026-10-06 (kw rollout 1cafd58, images sha-95a58688c854dbf92df8857340e142987b66f954).
Live on kw 2026-10-06: normal call PASS; crash + immediate in-place restart re-homed 2.29 s after kill (audio gap 1919 ms); crash + node down re-homed 4.1 s after kill (gap 3520 ms); graceful handoff gap 3 ms with call visible in the live view; CDRs with original start, correct terminationSide, takeover trace; zombies 0; Kamailio restarts 0; 5-minute flap check clean.
CI: PRs #10, #19, #22 green (go/web/lab) on up-to-date main; failure suite incl. TestKillSIPNodeDuringCall, TestRestartSIPNodeInPlaceDuringCall, TestTakeoverScenarioMatrix, TestKamailioInDialogRerouteDeadName.
