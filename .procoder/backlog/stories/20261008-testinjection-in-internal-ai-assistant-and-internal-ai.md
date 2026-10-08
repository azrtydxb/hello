# `TestInjection` in `internal/ai/assistant` and `internal/ai/proposal` passes. Fixtures put instructions in User-Agents, caller names, dialled numbers and tool results; it fails if any fixture yields a stored proposal outside the allowlist, a body with a credential property, a tool call outside `assistantTools`, or an assistant message containing HTML or a Markdown link the console would render.

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

- [ ] `TestInjection` in `internal/ai/assistant` and `internal/ai/proposal` passes. Fixtures put instructions in User-Agents, caller names, dialled numbers and tool results; it fails if any fixture yields a stored proposal outside the allowlist, a body with a credential property, a tool call outside `assistantTools`, or an assistant message containing HTML or a Markdown link the console would render.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:b711f42043bc1b7eb73ffba5a85ceee604fe5137d067e24b9d3e2026dc203099
Produced: 1131 bytes, exit 0
Command: go test -race -count=1 -run ^TestInjection$ -v ./internal/ai/assistant/

Assistant half (Task 4): injections in User-Agents, caller names, dialled numbers, tool results and history never yield a non-GET replay, a stored proposal outside the allowlist or with a credential, a call outside assistantTools, or a stored HTML/Markdown-link answer; tool results reach the model as one closed data block. The proposal half is Task 3; open until it lands.
