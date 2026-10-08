# `TestGenerate` in `internal/ai` passes. It fails if a system prompt lacks the feature line or the untrusted-data notice, an invalid answer is stored instead of retried, a fourth attempt is made, a `length` finish is not retried once with double tokens, reasoning text is stored, or `DataBlock` lets a string close the block.

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

- [ ] `TestGenerate` in `internal/ai` passes. It fails if a system prompt lacks the feature line or the untrusted-data notice, an invalid answer is stored instead of retried, a fourth attempt is made, a `length` finish is not retried once with double tokens, reasoning text is stored, or `DataBlock` lets a string close the block.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
