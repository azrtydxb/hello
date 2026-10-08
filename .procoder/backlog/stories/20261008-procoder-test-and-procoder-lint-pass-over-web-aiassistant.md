# `procoder test` and `procoder lint` pass over `web/`. `AIAssistant.test.tsx`, `AIFindings.test.tsx`, `AIProposals.test.tsx` and `AIStatus.test.tsx` fail if a viewer sees chat or apply/dismiss controls, assistant text is rendered as HTML or Markdown links, the diff does not mark fields where `current` differs from `before`, apply does not name each operation before confirming, dismiss is sent without a reason, or the status page shows a URL path or key.

Status: open
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Spec ai-agent S-22, S-23; plan ai-agent Task 6. Done when the named check passes and fails on each break it lists, so the behaviour of S-22, S-23 cannot regress silently.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder test` and `procoder lint` pass over `web/`. `AIAssistant.test.tsx`, `AIFindings.test.tsx`, `AIProposals.test.tsx` and `AIStatus.test.tsx` fail if a viewer sees chat or apply/dismiss controls, assistant text is rendered as HTML or Markdown links, the diff does not mark fields where `current` differs from `before`, apply does not name each operation before confirming, dismiss is sent without a reason, or the status page shows a URL path or key.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
