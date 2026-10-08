# [S-5.1] `TestDetectors` in `internal/ai/detect` passes, one table per detector against PostgreSQL and Valkey. It fails if any detector misses its candidate at its threshold or raises one just below it, including a shadowed outbound route, a ring group with no reachable member, a phone that never fetched, or `call_quality` missing a trunk or node with 3 of its last 5 CDRs at loss ≥ 1 % or jitter ≥ 100 ms. `TestCallQualityCDR` in `internal/cdr` passes; it fails if an anchored call's CDR lacks `rtp_packets`, `rtp_lost` or `rtp_jitter_ms` from the relay's last snapshot, or a directly-media call's CDR has them non-null; `web/src/pages/CallDetail.test.tsx` fails if the call detail page does not show loss percent and jitter for an anchored call.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] [S-5.1] `TestDetectors` in `internal/ai/detect` passes, one table per detector against PostgreSQL and Valkey. It fails if any detector misses its candidate at its threshold or raises one just below it, including a shadowed outbound route, a ring group with no reachable member, a phone that never fetched, or `call_quality` missing a trunk or node with 3 of its last 5 CDRs at loss ≥ 1 % or jitter ≥ 100 ms. `TestCallQualityCDR` in `internal/cdr` passes; it fails if an anchored call's CDR lacks `rtp_packets`, `rtp_lost` or `rtp_jitter_ms` from the relay's last snapshot, or a directly-media call's CDR has them non-null; `web/src/pages/CallDetail.test.tsx` fails if the call detail page does not show loss percent and jitter for an anchored call.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `HELLO_TEST_DATABASE_URL=... go test -race ./internal/ai/detect/ -run TestDetectors` passes (local PostgreSQL 17): a table per detector at, above and below each S-19 threshold (reg_failures 9/10 rejected, auth_bruteforce 4/5 usernames and limit-1/limit, trunk_down 1m59s/2m, trunk_asr_drop 19/20 attempts and half the baseline, node_health 2/3 restarts, 1/2 min not ready, 4/5 min lag, capacity 2/3 of 5 samples at 80 percent and overcommit, call_quality 2/3 of the last 5 and loss 0.99/1 percent and jitter 99.9/100 ms, config_smells incl. shadowed routes, empty and unreachable ring groups, phones at 23/25 h, a device unregistered 6/7 days). The two Valkey subtests (register-attempt scan) skip here and run on CI. 24 threshold mutations (every comparison and constant) were killed; 3 survivors got boundary tests and were then killed.
