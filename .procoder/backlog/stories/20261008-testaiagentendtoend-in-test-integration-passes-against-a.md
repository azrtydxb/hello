# `TestAIAgentEndToEnd` in `test/integration` passes against a scripted OpenAI-compatible fixture. It fails if a chat question does not produce a tool call replayed as the user and a proposal with a diff, applying it does not change the configuration with an audit row, a seeded REGISTER flood does not produce an explained `auth_bruteforce` finding, or a dismissed proposal can be applied.

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

- [ ] `TestAIAgentEndToEnd` in `test/integration` passes against a scripted OpenAI-compatible fixture. It fails if a chat question does not produce a tool call replayed as the user and a proposal with a diff, applying it does not change the configuration with an audit row, a seeded REGISTER flood does not produce an explained `auth_bruteforce` finding, or a dismissed proposal can be applied.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
