# `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`, against the real fastllm on kw) passes. It fails if a structured explanation, a tool call and a final structured answer do not each succeed with the deployed settings.

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

- [x] `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`, against the real fastllm on kw) passes. It fails if a structured explanation, a tool call and a final structured answer do not each succeed with the deployed settings.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- `TestKwSmokeAI` run 2026-10-14 from a busybox pod on kw (deployment
  f35d86c, hello-ai secret settings, HELLO_KW_SMOKE=1): structured
  explanation answered in 15.8s, tool call plus final structured answer in
  12.7s, both PASS — the fastllm outage from 2026-10-09 is over.
