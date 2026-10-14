# `TestAIAgentEndToEnd` in `test/integration` passes against a scripted OpenAI-compatible fixture. It fails if a chat question does not produce a tool call replayed as the user and a proposal with a diff, applying it does not change the configuration with an audit row, a seeded REGISTER flood does not produce an explained `auth_bruteforce` finding, or a dismissed proposal can be applied.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An operator using the in-product AI agent needs the whole path to work together on a real lab cluster, not only package by package: ask a question, see the tool call made as them, get a proposal with a diff, apply it with audit, and see a REGISTER flood explained as a finding (spec ai-agent S-6, S-13, S-18). Done when the lab test proves that path against a scripted OpenAI-compatible model.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAIAgentEndToEnd` in `test/integration` passes against a scripted OpenAI-compatible fixture. It fails if a chat question does not produce a tool call replayed as the user and a proposal with a diff, applying it does not change the configuration with an audit row, a seeded REGISTER flood does not produce an explained `auth_bruteforce` finding, or a dismissed proposal can be applied.

## Evidence

- `TestAIAgentEndToEnd` (test/integration/ai_agent_test.go:117) ran in the lab job against test/fakellm: `--- PASS: TestAIAgentEndToEnd (112.35s)` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and `(120.16s)` in PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`). It asserts the listExtensions tool call is replayed as the user (status 200, cited in the answer), the proposal has before/after and a marked delete, apply changes the ring groups and raises the config revision with >= 2 audit rows `via ai-proposal:<id>`, the REGISTER flood yields an open, explained `auth_bruteforce` finding, and apply of a dismissed proposal answers 409.
