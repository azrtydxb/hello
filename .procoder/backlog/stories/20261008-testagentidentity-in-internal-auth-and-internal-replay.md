# `TestAgentIdentity` in `internal/auth` and `internal/replay` passes. It fails if a request header can set the agent marker, an agent replay gets any scope but `read`, a deleted user's agent replay is admitted, a demotion does not apply on the next call, a replay can start a replay, or an agent read's log line lacks `via=ai-assistant`.

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

- [ ] `TestAgentIdentity` in `internal/auth` and `internal/replay` passes. It fails if a request header can set the agent marker, an agent replay gets any scope but `read`, a deleted user's agent replay is admitted, a demotion does not apply on the next call, a replay can start a replay, or an agent read's log line lacks `via=ai-assistant`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
