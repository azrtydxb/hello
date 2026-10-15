# `TestInjection` in `internal/ai/assistant` and `internal/ai/proposal` passes. Fixtures put instructions in User-Agents, caller names, dialled numbers and tool results; it fails if any fixture yields a stored proposal outside the allowlist, a body with a credential property, a tool call outside `assistantTools`, or an assistant message containing HTML or a Markdown link the console would render.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An admin running Hello on untrusted SIP traffic needs text from callers and phones (User-Agents, caller names, dialled numbers) to stay data: it must never make the assistant change configuration, call a forbidden tool or render links in the console (spec S-5, S-6, S-12). Done when injection fixtures cannot produce any of those effects.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestInjection` in `internal/ai/assistant` and `internal/ai/proposal` passes. Fixtures put instructions in User-Agents, caller names, dialled numbers and tool results; it fails if any fixture yields a stored proposal outside the allowlist, a body with a credential property, a tool call outside `assistantTools`, or an assistant message containing HTML or a Markdown link the console would render.

## Evidence

- `TestInjection` in internal/ai/assistant/injection_test.go:34 (User-Agent, caller name, dialled number, history and tool-result fixtures; checks no non-GET replay, no tool outside assistantTools, no stored HTML/Markdown link or image, untrusted text only inside one closed data block) and internal/ai/proposal/injection_test.go:17 (against PostgreSQL: only the allowlisted route to +900 is stored, no credential property, injected text stays data). Both packages `ok` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`); the proposal half needs HELLO_TEST_DATABASE_URL, which the go job sets.
- Per-condition audit 2026-10-15: gap fixed: the assistant half only checked that replays were GETs, so a read outside assistantTools (listUsers, listTokens) would pass; a fixture now calls them and every replayed path must belong to an assistantTools operation. Allowlist, credential and markup checks were already asserted in both packages.
