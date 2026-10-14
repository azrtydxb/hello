# `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`, against the real fastllm on kw) passes. It fails if a structured explanation, a tool call and a final structured answer do not each succeed with the deployed settings.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

The kw deployment must work with the real model server it is configured for, not only the fake (spec S-2, S-27). Done when a structured explanation, a tool call and a final structured answer each succeed against fastllm with the deployed settings.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`, against the real fastllm on kw) passes. It fails if a structured explanation, a tool call and a final structured answer do not each succeed with the deployed settings.

## Evidence

- `TestKwSmokeAI` (test/integration/kw_smoke_ai_test.go:49) passed live on kw against fastllm on 2026-10-14 with the deployed settings and thinking off: 5/5 runs (PR #85 report), structured explanation, tool call and final structured answer each succeeded. These runs used the PR #85 version of the test, which also covers the criterion as the spec extended it on 2026-10-14: the assistant's system prompt and answer type through the tool and answer phases must call `listRegistrations` and cite it.
