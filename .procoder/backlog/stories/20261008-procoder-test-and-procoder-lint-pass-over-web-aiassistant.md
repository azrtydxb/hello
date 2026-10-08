# `procoder test` and `procoder lint` pass over `web/`. `AIAssistant.test.tsx`, `AIFindings.test.tsx`, `AIProposals.test.tsx` and `AIStatus.test.tsx` fail if a viewer sees chat or apply/dismiss controls, assistant text is rendered as HTML or Markdown links, the diff does not mark fields where `current` differs from `before`, apply does not name each operation before confirming, dismiss is sent without a reason, or the status page shows a URL path or key.

Status: done 2026-10-08
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

As an operator I use the console's AI pages (assistant, findings, proposals, status) and as a viewer I read them without being able to change anything. Done: the pages render model and network text as plain text, mark every field where the target changed since the proposal, name each operation before an apply, mark and double-confirm deletes, require a reason to dismiss, and the status page shows only the endpoint host.

## Acceptance criteria

- [x] `procoder test` and `procoder lint` pass over `web/`. `AIAssistant.test.tsx`, `AIFindings.test.tsx`, `AIProposals.test.tsx` and `AIStatus.test.tsx` fail if a viewer sees chat or apply/dismiss controls, assistant text is rendered as HTML or Markdown links, the diff does not mark fields where `current` differs from `before`, apply does not name each operation before confirming, dismiss is sent without a reason, or the status page shows a URL path or key.

## Evidence

- `pnpm -C web typecheck`, `pnpm -C web lint` clean; `pnpm -C web test`: 35 files, 248 tests pass, including AIAssistant (plain text, no img/link from model output, viewer has no composer, 2 s task polling, 4000 limit), AIFindings (viewer has no acknowledge/dismiss, dismiss needs a reason), AIProposals (`changed since` marks only drifted fields, apply dialog lists each operation, dismiss always sends a reason, failure names applied actions, TestProposalDeleteUI), AIStatus (no URL or key text, viewer has no Run now, off state shows the reason).
- After merging main: `pnpm -C web typecheck`, `lint` clean; `pnpm -C web test`: 36 files, 261 tests pass. New `aiagent.contract.test.ts` (TestWebTypesMatchOpenAPI) fails if a web AI type's properties differ from openapi.json (found and fixed: proposal `destructive`/`references`, finding history and actor fields, task `result`/`startedAt`/`finishedAt`, proposal actor fields, the optimistic task kind `message`); AIProposals tests show a delete's references as plain text and mark an action the server calls destructive whatever its name.
- `procoder check`: 0 blocking.
