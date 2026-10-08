# `TestProposalValidation` in `internal/ai/proposal` passes. It fails if an operation outside the allowlist, an allowlisted operation that is not `write` or returns a secret, a body with an unknown or credential property, a path parameter naming a missing row, or more than 8 actions is stored. `TestProposalDeleteValidation` passes; it fails if a delete proposal is not visually marked, does not require an explicit confirmation, or fails to show what references the deleted resource in the diff.

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

- [ ] `TestProposalValidation` in `internal/ai/proposal` passes. It fails if an operation outside the allowlist, an allowlisted operation that is not `write` or returns a secret, a body with an unknown or credential property, a path parameter naming a missing row, or more than 8 actions is stored. `TestProposalDeleteValidation` passes; it fails if a delete proposal is not visually marked, does not require an explicit confirmation, or fails to show what references the deleted resource in the diff.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
