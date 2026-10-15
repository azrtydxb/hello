# `docs/ai-agent.md` exists and covers every item of S-28; `TestDocsAIAgent` in `test/deploy` fails if its tool list, allowlist or detector thresholds differ from the code, or the README does not link it.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An admin enabling the AI agent needs one guide covering setup, privacy, what the assistant can do, proposals, detectors, budgets, retention and troubleshooting (spec S-28), and that guide must not drift from the code. Done when docs/ai-agent.md covers S-28 and a test fails on stale tables, settings or error codes.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `docs/ai-agent.md` exists and covers every item of S-28; `TestDocsAIAgent` in `test/deploy` fails if its tool list, allowlist or detector thresholds differ from the code, or the README does not link it.

## Evidence

- docs/ai-agent.md still covers S-28 after the streaming, thinking and tool/answer split changes (sections: Enable it, Privacy and public endpoints, What the assistant can and cannot do, Streaming answers, Proposals and the allowlist, Detectors and findings with thresholds and lifecycle, Budgets and metrics, Retention, Troubleshooting by error code). `TestDocsAIAgent` (test/deploy/ai_test.go:230) compares the tool, allowlist and threshold tables with doctable output, checks the README and docs/ai-access.md links, and now also requires every HELLO_AI_ setting in internal/config/config.go and every AI error code in internal/ai to be documented. `ok test/deploy` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: every condition asserted by TestDocsAIAgent (test/deploy): S-28 sections, tool/allowlist/threshold tables against doctable, every HELLO_AI_ setting and AI error code, README and docs/ai-access.md links; no gap.
