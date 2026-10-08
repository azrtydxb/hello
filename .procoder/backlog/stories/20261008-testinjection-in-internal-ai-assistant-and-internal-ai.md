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
