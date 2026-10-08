# `TestTasksAndLimits` in `internal/ai` passes with Valkey. It fails if a task of a replica whose heartbeat stopped is not failed `instance_stopped` within 15 s, another user reads a task, a third concurrent call starts, an interactive request waits more than 5 s before `429` `ai_busy`, background work runs past 80 % of the budget, or interactive work runs past 100 %.

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

- [ ] `TestTasksAndLimits` in `internal/ai` passes with Valkey. It fails if a task of a replica whose heartbeat stopped is not failed `instance_stopped` within 15 s, another user reads a task, a third concurrent call starts, an interactive request waits more than 5 s before `429` `ai_busy`, background work runs past 80 % of the budget, or interactive work runs past 100 %.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
