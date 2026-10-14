# `TestTrunkDialingValidation` in `internal/api` passes. It fails if an unknown mode, an uncompilable or oversized pattern, more than 100 patterns, or `patterns` set with mode `off` is accepted, or if mode `all` is saved without `"confirm": true`.

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an administrator, I need the API to refuse invalid internal dialing settings and to require confirmation for mode all, so that a trunk is never opened up by accident or by a malformed pattern.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestTrunkDialingValidation` in `internal/api` passes. It fails if an unknown mode, an uncompilable or oversized pattern, more than 100 patterns, or `patterns` set with mode `off` is accepted, or if mode `all` is saved without `"confirm": true`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/api/trunks_test.go:45 TestTrunkDialingValidation (DB-backed via newP2Env; runs in CI with HELLO_TEST_DATABASE_URL). main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/internal/api	302.708s`.

- Per-condition audit 2026-10-15: unknown mode accepted -> internal/api/trunks_test.go:50; uncompilable pattern -> :64; oversized (>500) pattern -> :56; more than 100 patterns -> :62; patterns with mode off -> :52; mode all saved without confirm -> :54 (absent), :55 (false) on POST and added :112-118 (PATCH to mode all without confirm is refused 400 on internalDialing.confirm and GET shows mode still off). refusedDialing -> fieldPaths (routing_test.go:191) fails unless the response is 400 bad_request and names the field. DB-backed (newP2Env): skips locally, runs in CI.
