# `docs/ai-access.md` exists and covers every item of S-21; `TestDocsAIAccess` in `test/deploy` fails if its tool and resource tables differ from what hello-control generates, or the README does not link it.

Status: done 2026-10-08
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

An operator connecting Claude Code, Claude Desktop or another MCP client finds everything in docs/ai-access.md, linked from the README: trusting cluster-ca, the scopes and roles, consent and revocation, service accounts, the generated tool and resource tables, the skills and the security model (spec S-21; plan Task 7).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `docs/ai-access.md` exists and covers every item of S-21; `TestDocsAIAccess` in `test/deploy` fails if its tool and resource tables differ from what hello-control generates, or the README does not link it.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:3565336d0020b579b61554e221cdcd880406a4a4934b0a145b55e73b1dc1e8a5
Produced: 117 bytes, exit 0
Command: go test -count=1 -run ^TestDocsAIAccess$ -v ./test/deploy/

Mutation-checked: changing one generated row's scope fails it (tables compared cell by cell, so the formatter's alignment is not drift).
