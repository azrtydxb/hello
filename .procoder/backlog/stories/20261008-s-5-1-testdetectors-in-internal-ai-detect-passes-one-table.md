# [S-5.1] `TestDetectors` in `internal/ai/detect` passes, one table per detector against PostgreSQL and Valkey. It fails if any detector misses its candidate at its threshold or raises one just below it, including a shadowed outbound route, a ring group with no reachable member, a phone that never fetched, or `call_quality` missing a trunk or node with 3 of its last 5 CDRs at loss ≥ 1 % or jitter ≥ 100 ms. `TestCallQualityCDR` in `internal/cdr` passes; it fails if an anchored call's CDR lacks `rtp_packets`, `rtp_lost` or `rtp_jitter_ms` from the relay's last snapshot, or a directly-media call's CDR has them non-null; `web/src/pages/CallDetail.test.tsx` fails if the call detail page does not show loss percent and jitter for an anchored call.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator relying on AIOps findings needs each detector to fire exactly at its documented threshold and not just below it, including per-call quality from CDRs (spec S-5.1, S-18 to S-20). Done when every detector has a threshold table against PostgreSQL and Valkey and the CDR quality columns are recorded and shown.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] [S-5.1] `TestDetectors` in `internal/ai/detect` passes, one table per detector against PostgreSQL and Valkey. It fails if any detector misses its candidate at its threshold or raises one just below it, including a shadowed outbound route, a ring group with no reachable member, a phone that never fetched, or `call_quality` missing a trunk or node with 3 of its last 5 CDRs at loss ≥ 1 % or jitter ≥ 100 ms. `TestCallQualityCDR` in `internal/cdr` passes; it fails if an anchored call's CDR lacks `rtp_packets`, `rtp_lost` or `rtp_jitter_ms` from the relay's last snapshot, or a directly-media call's CDR has them non-null; `web/src/pages/CallDetail.test.tsx` fails if the call detail page does not show loss percent and jitter for an anchored call.

## Evidence

- `TestDetectors` (internal/ai/detect/detectors_test.go:31, with detectors_more_test.go: shadowed routes, unreachable ring group, never-fetched phone, call_quality 3 of last 5 by loss/jitter per trunk and node) and `TestCallQualityCDR` (internal/cdr/cdr_test.go:230) pass against PostgreSQL and Valkey: `ok internal/ai/detect`, `ok internal/cdr` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`). `web/src/pages/CallDetail.test.tsx` passes in the web job of PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
