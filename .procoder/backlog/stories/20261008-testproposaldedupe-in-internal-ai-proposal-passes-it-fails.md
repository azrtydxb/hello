# `TestProposalDedupe` in `internal/ai/proposal` passes. It fails if an identical open proposal is duplicated, a newer proposal on the same targets does not supersede the older, or a dismissed fingerprint is proposed again within 7 days.

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

- [ ] `TestProposalDedupe` in `internal/ai/proposal` passes. It fails if an identical open proposal is duplicated, a newer proposal on the same targets does not supersede the older, or a dismissed fingerprint is proposed again within 7 days.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
