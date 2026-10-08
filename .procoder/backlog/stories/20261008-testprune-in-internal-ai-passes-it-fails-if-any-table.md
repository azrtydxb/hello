# `TestPrune` in `internal/ai` passes. It fails if any table keeps rows past its retention, prunes inside it, or two replicas prune at once.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-26; plan ai-agent Task 2. Done when the named check passes and fails on each break it lists, so the behaviour of S-26 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestPrune` in `internal/ai` passes. It fails if any table keeps rows past its retention, prunes inside it, or two replicas prune at once.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
