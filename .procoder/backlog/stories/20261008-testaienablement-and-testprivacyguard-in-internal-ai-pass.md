# `TestAIEnablement` and `TestPrivacyGuard` in `internal/ai` pass. They fail if AI turns on without both base URL and model, an AI operation answers other than `503` `ai_disabled` while off, a public or rebinding host is dialled without `HELLO_AI_ALLOW_PUBLIC_ENDPOINT`, the provider is built outside `provider.go`, or the API key appears in status, logs or metrics.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-1, S-2, S-3; plan ai-agent Task 2. Done when the named check passes and fails on each break it lists, so the behaviour of S-1, S-2, S-3 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestAIEnablement` and `TestPrivacyGuard` in `internal/ai` pass. They fail if AI turns on without both base URL and model, an AI operation answers other than `503` `ai_disabled` while off, a public or rebinding host is dialled without `HELLO_AI_ALLOW_PUBLIC_ENDPOINT`, the provider is built outside `provider.go`, or the API key appears in status, logs or metrics.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
