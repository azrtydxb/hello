# `TestProposalValidation` in `internal/ai/proposal` passes. It fails if an operation outside the allowlist, an allowlisted operation that is not `write` or returns a secret, a body with an unknown or credential property, a path parameter naming a missing row, or more than 8 actions is stored.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-10, S-11, S-12; plan ai-agent Task 3. Done when the named check passes and fails on each break it lists, so the behaviour of S-10, S-11, S-12 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestProposalValidation` in `internal/ai/proposal` passes. It fails if an operation outside the allowlist, an allowlisted operation that is not `write` or returns a secret, a body with an unknown or credential property, a path parameter naming a missing row, or more than 8 actions is stored.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
