# `TestAssistantSessions` in `internal/api` and `internal/ai/assistant` passes. It fails if a viewer can post a message, another non-admin user can read a session, a second message during a running task is accepted, more than 20 messages reach the model, an answer cites a tool call that was not made, or a proposal in an answer is stored unvalidated.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator chatting with the assistant needs private sessions, one answer at a time, a bounded history, and answers that only cite tool calls actually made with proposals validated before storing (spec S-8, S-9). Done when sessions are tested in the assistant package and over HTTP with PostgreSQL.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAssistantSessions` in `internal/api` and `internal/ai/assistant` passes. It fails if a viewer can post a message, another non-admin user can read a session, a second message during a running task is accepted, more than 20 messages reach the model, an answer cites a tool call that was not made, or a proposal in an answer is stored unvalidated.

## Evidence

- `TestAssistantSessions` in internal/ai/assistant/session_test.go:20 (ownership, one task per session, 20-message window as data, uncited calls rejected, proposals validated before storing) and internal/api/ai_sessions_test.go:60 (PostgreSQL over HTTP: viewer 403, other operator 404, 409 task_running, 20 messages reach the model, answer stored with its task). `ok internal/ai/assistant`, `ok internal/api` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: every condition asserted by both halves of TestAssistantSessions (internal/ai/assistant, internal/api): viewer 403, other user 404, 409 task_running, 20-message window, uncited calls never stored, proposals validated before storing; no gap.
