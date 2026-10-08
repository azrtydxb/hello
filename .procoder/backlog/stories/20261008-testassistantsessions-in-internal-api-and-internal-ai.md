# `TestAssistantSessions` in `internal/api` and `internal/ai/assistant` passes. It fails if a viewer can post a message, another non-admin user can read a session, a second message during a running task is accepted, more than 20 messages reach the model, an answer cites a tool call that was not made, or a proposal in an answer is stored unvalidated.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-8, S-9; plan ai-agent Task 4. Done when the named check passes and fails on each break it lists, so the behaviour of S-8, S-9 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestAssistantSessions` in `internal/api` and `internal/ai/assistant` passes. It fails if a viewer can post a message, another non-admin user can read a session, a second message during a running task is accepted, more than 20 messages reach the model, an answer cites a tool call that was not made, or a proposal in an answer is stored unvalidated.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
