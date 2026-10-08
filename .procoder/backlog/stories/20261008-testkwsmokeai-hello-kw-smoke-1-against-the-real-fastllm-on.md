# `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`, against the real fastllm on kw) passes. It fails if a structured explanation, a tool call and a final structured answer do not each succeed with the deployed settings.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-2, S-27; plan ai-agent Task 7. Done when the named check passes and fails on each break it lists, so the behaviour of S-2, S-27 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestKwSmokeAI` (`HELLO_KW_SMOKE=1`, against the real fastllm on kw) passes. It fails if a structured explanation, a tool call and a final structured answer do not each succeed with the deployed settings.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
