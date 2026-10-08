# `TestProposalApply` in `internal/api` passes. It fails if apply replays with anything but the applier's `Cookie`/`Authorization`, a viewer can apply or dismiss, a changed target is applied instead of `stale`, an unrelated revision change makes it stale, a failing action does not stop the sequence with its index and status recorded, a concurrent apply is not `409` `proposal_not_open`, dismiss accepts no reason, or an action's audit row lacks `via` `ai-proposal:<id>`.

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

- [ ] `TestProposalApply` in `internal/api` passes. It fails if apply replays with anything but the applier's `Cookie`/`Authorization`, a viewer can apply or dismiss, a changed target is applied instead of `stale`, an unrelated revision change makes it stale, a failing action does not stop the sequence with its index and status recorded, a concurrent apply is not `409` `proposal_not_open`, dismiss accepts no reason, or an action's audit row lacks `via` `ai-proposal:<id>`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
