# `TestDetectorLatency` (`HELLO_BENCH=1`) passes. It fails if any detector takes 2 s or more over 1 million CDRs and 10 000 devices.

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

- [x] `TestDetectorLatency` (`HELLO_BENCH=1`) passes. It fails if any detector takes 2 s or more over 1 million CDRs and 10 000 devices.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `HELLO_BENCH=1 HELLO_TEST_DATABASE_URL=... go test ./internal/ai/detect/ -run TestDetectorLatency -v`: over 1 000 000 generated CDRs and 10 000 devices every DB detector took 0.3 s or less (trunk_asr_drop 299 ms, call_quality 291 ms, config_smells 116 ms, others under 10 ms), with and without the candidate indexes, so none is added to 00009. reg_failures and auth_bruteforce are measured only where HELLO_TEST_VALKEY_ADDR is set (CI).
