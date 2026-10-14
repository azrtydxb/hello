# `TestDetectorLatency` (`HELLO_BENCH=1`) passes. It fails if any detector takes 2 s or more over 1 million CDRs and 10 000 devices.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator of a large installation needs the AIOps detectors to stay cheap at scale (spec S-19): each under 2 s over 1 million CDRs and 10 000 devices. Done when the benchmark passes in CI on PostgreSQL and Valkey.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestDetectorLatency` (`HELLO_BENCH=1`) passes. It fails if any detector takes 2 s or more over 1 million CDRs and 10 000 devices.

## Evidence

- `TestDetectorLatency` (internal/ai/detect/bench_test.go:20) ran in the new dispatch-only `bench` job of .github/workflows/ci.yaml (PostgreSQL + Valkey, HELLO_BENCH=1): run 38633658726 (`gh run view 38633658726`), `--- PASS: TestDetectorLatency (18.65s)` over 1 000 000 CDRs and 10 000 devices, every detector measured including the Valkey ones: reg_failures 103 ms, auth_bruteforce 53 ms, trunk_down 24 ms, trunk_asr_drop 270 ms, node_health 1 ms, trunk_capacity 24 ms, call_quality 238 ms, config_smells 325 ms, all under the 2 s limit.
