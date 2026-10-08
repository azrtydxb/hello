# `TestAIMetrics` in `internal/ai` passes, and `TestNoSecretsInLogs` covers AI. They fail if a call, tool call, run, finding or proposal change does not move its metric, or if an API key, a prompt, a response or a tool result appears in a log line.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-25; plan ai-agent Task 2. Done when the named check passes and fails on each break it lists, so the behaviour of S-25 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestAIMetrics` in `internal/ai` passes, and `TestNoSecretsInLogs` covers AI. They fail if a call, tool call, run, finding or proposal change does not move its metric, or if an API key, a prompt, a response or a tool result appears in a log line.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
