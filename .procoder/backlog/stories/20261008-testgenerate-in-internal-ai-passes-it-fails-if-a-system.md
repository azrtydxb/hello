# `TestGenerate` in `internal/ai` passes. It fails if a system prompt lacks the feature line or the untrusted-data notice, an invalid answer is stored instead of retried, a fourth attempt is made, a `length` finish is not retried once with double tokens, reasoning text is stored, or `DataBlock` lets a string close the block.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

Every AI feature depends on one structured model call that marks untrusted data, validates and retries answers within bounds, and never keeps model reasoning (spec S-4, S-5). Done when the shared Generate path is tested for prompts, retries, length handling, reasoning and data-block escaping.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestGenerate` in `internal/ai` passes. It fails if a system prompt lacks the feature line or the untrusted-data notice, an invalid answer is stored instead of retried, a fourth attempt is made, a `length` finish is not retried once with double tokens, reasoning text is stored, or `DataBlock` lets a string close the block.

## Evidence

- `TestGenerate` (internal/ai/generate_test.go:50): feature line and untrusted-data notice in the system prompt, invalid answers retried with the error and no fourth attempt, a length finish retried once at double max_tokens, reasoning neither in the answer, the retry transcript nor anything the store holds (new check), `TestDataBlock` for DataBlock escaping. `ok internal/ai` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
