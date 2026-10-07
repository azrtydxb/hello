# `TestOpenAPIForTools` in `internal/api` passes. It fails if an operation lacks a summary, a description of at least one sentence, a description on any parameter or top-level body property, `x-hello-scope`, or `x-hello-mcp`, or if a response property carrying a show-once secret lacks `x-hello-secret`.

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

- [ ] `TestOpenAPIForTools` in `internal/api` passes. It fails if an operation lacks a summary, a description of at least one sentence, a description on any parameter or top-level body property, `x-hello-scope`, or `x-hello-mcp`, or if a response property carrying a show-once secret lacks `x-hello-secret`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
