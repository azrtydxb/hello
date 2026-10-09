# `TestTrunkInternalLab` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if a fakecarrier INVITE to `sip:1012@…` on a trunk with pattern `1XX` does not ring extension 1012 and carry audio both ways, if a DID outside the pattern is not rejected, or if the trace lacks the verdict.

Status: open
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestTrunkInternalLab` in `test/integration` (`HELLO_DOCKER=1`) passes. It fails if a fakecarrier INVITE to `sip:1012@…` on a trunk with pattern `1XX` does not ring extension 1012 and carry audio both ways, if a DID outside the pattern is not rejected, or if the trace lacks the verdict.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
