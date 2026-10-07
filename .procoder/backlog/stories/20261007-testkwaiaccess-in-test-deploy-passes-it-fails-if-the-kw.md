# `TestKwAIAccess` in `test/deploy` passes. It fails if the kw manifest lacks the `hello-tls` certificate from `cluster-ca` on the `hello` Ingress, `HELLO_PUBLIC_URL` on hello-control, `HELLO_OAUTH_DCR=true`, or the nginx template lacks the `/mcp`, `/oauth/` and `/.well-known/oauth-` locations.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestKwAIAccess` in `test/deploy` passes. It fails if the kw manifest lacks the `hello-tls` certificate from `cluster-ca` on the `hello` Ingress, `HELLO_PUBLIC_URL` on hello-control, `HELLO_OAUTH_DCR=true`, or the nginx template lacks the `/mcp`, `/oauth/` and `/.well-known/oauth-` locations.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
