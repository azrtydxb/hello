# `TestAIMetrics` in `internal/ai`, `internal/ai/detect` and `internal/ai/proposal` and `TestAssistantTools` in `internal/ai/assistant` (tool calls) pass, and `TestNoSecretsInLogs` in `test/integration` covers AI. They fail if a call, tool call, run, finding or proposal change does not move its metric, or if an API key, a prompt, a response or a tool result appears in a log line.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator monitoring Hello needs AI calls, tool calls, runs, findings and proposals visible in metrics, and an auditor needs keys, prompts, responses and tool results kept out of logs (spec S-25). Done when each metric is proven to move and the lab log scan finds no AI secret or marker.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAIMetrics` in `internal/ai`, `internal/ai/detect` and `internal/ai/proposal` and `TestAssistantTools` in `internal/ai/assistant` (tool calls) pass, and `TestNoSecretsInLogs` in `test/integration` covers AI. They fail if a call, tool call, run, finding or proposal change does not move its metric, or if an API key, a prompt, a response or a tool result appears in a log line.

## Evidence

- `TestAIMetrics` (internal/ai/metrics_test.go:22: calls, busy, tokens, runs, budget; no key/prompt/data/response in logs or metrics) plus `TestAIMetrics` in internal/ai/detect (metrics_test.go:21: an aiops run observes hello_ai_detector_seconds per detector and moves hello_ai_findings on appear/resolve) and `TestAIMetrics` in internal/ai/proposal (metrics_test.go:19: store, apply and dismiss move hello_ai_proposals_total); tool calls via TestAssistantTools/TestToolCallMetricRegisteredOnce. `TestNoSecretsInLogs` (test/integration/zz_last_test.go:18) now checks the lab API key, a prompt marker, a tool-result marker and a model-response marker from TestAIAgentEndToEnd: `--- PASS: TestNoSecretsInLogs` in the lab job of PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: gap fixed in text and names: finding, proposal and tool-call metrics were asserted outside internal/ai under other names; the detect and proposal halves are now TestAIMetrics in their packages and the story names them and TestAssistantTools (tool calls). Key/prompt/response/tool-result log checks are in TestAIMetrics and TestNoSecretsInLogs; no other gap.
