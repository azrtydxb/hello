# `TestAgentIdentity` in `internal/auth` and `internal/replay` passes. It fails if a request header can set the agent marker, an agent replay gets any scope but `read`, a deleted user's agent replay is admitted, a demotion does not apply on the next call, a replay can start a replay, or an agent read's log line lacks `via=ai-assistant`.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

A security-minded admin needs the assistant's reads to run as the asking user, read-only, with their current role, and to be traceable in logs (spec S-7); nobody outside the process may forge that identity and a replay may not chain into another. Done when the auth middleware and the replay path are both tested for this.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAgentIdentity` in `internal/auth` and `internal/replay` passes. It fails if a request header can set the agent marker, an agent replay gets any scope but `read`, a deleted user's agent replay is admitted, a demotion does not apply on the next call, a replay can start a replay, or an agent read's log line lacks `via=ai-assistant`.

## Evidence

- `TestAgentIdentity` in internal/auth/agent_test.go:15 (headers cannot set the marker, read-only scope, demotion, deleted user 401) and the new replay half internal/replay/agent_test.go:77 (agent replays through replay.Do into auth.Middleware: header-only marker refused 401, read scope only, nested replay refused with ErrNested, demotion applies on the next call, deleted user 401, and the `agent read` log line carries `via=ai-assistant` with actor and task, added in internal/auth/middleware.go). `ok internal/auth`, `ok internal/replay` in PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: every condition asserted by both halves of TestAgentIdentity (internal/auth, internal/replay): header marker 401, read-only scope, deleted user 401, demotion on the next call, nested replay ErrNested, via=ai-assistant log line; no gap.
