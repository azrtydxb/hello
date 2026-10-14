# [S-4] [S-8] `TestGenerateSplit` in `internal/ai` passes. It fails if a call with tools asks for JSON or sends `response_format` while tools are offered, offers tools in the answer phase, does not list the calls made for citations, nudges a model that describes a tool call more than once or not at all, nudges past the step budget, does not apply the structured-output mode and validation retries to the answer phase, or, streamed, sends tool-phase text or a `reset` before the answer's deltas.

Status: done 2026-10-14
Created: 2026-10-14
Epic: ai-agent
Sprint: -

## Description

An operator asking the assistant with model thinking off needs the model to actually use its tools, which it skips when the same request also demands a JSON answer (decision 2026-10-14, "split tool calling from the structured answer"). The spec's S-4 and S-8 now split a call with tools into a tool phase (tools, plain text, no structured-output instruction) and an answer phase (no tools, the structured answer with validation and retries), and the stream only carries the answer phase. This story was added after the epic was seeded, when PR #85 added the criterion to the spec. Done when `TestGenerateSplit` enforces the split in both structured-output modes and when streamed.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] [S-4] [S-8] `TestGenerateSplit` in `internal/ai` passes. It fails if a call with tools asks for JSON or sends `response_format` while tools are offered, offers tools in the answer phase, does not list the calls made for citations, nudges a model that describes a tool call more than once or not at all, nudges past the step budget, does not apply the structured-output mode and validation retries to the answer phase, or, streamed, sends tool-phase text or a `reset` before the answer's deltas.

## Evidence

- `TestGenerateSplit` (internal/ai/split_test.go:63, added by PR #85) covers both `json_schema` and `prompt` modes, the one-nudge rule, the step budget, citations by position and the streamed phases. `ok internal/ai` in PR #85 CI run 38630616864 (go + web + lab green, `gh run view 38630616864`) and in the rebased PR #90 CI; locally `go test -count=1 -run TestGenerateSplit ./internal/ai/` passes.
