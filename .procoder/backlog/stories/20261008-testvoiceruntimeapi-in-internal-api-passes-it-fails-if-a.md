# `TestVoiceRuntimeAPI` in `internal/api` passes. It fails if a token without `voice-runtime` reads it, a `voice-runtime` token reads anything else, OAuth consent can grant the scope, the response lacks an unsealed credential or an ETag, `If-None-Match` does not answer `304`, a long poll does not return within `wait` or on a revision change within 2 s, a disabled agent appears, or the operation is reachable through MCP, the assistant or a proposal.

Status: cancelled 2026-10-09 — the voice-agent feature left Hello (decision "Pivot: voice agents out of Hello", .procoder/ask/decisions.md)
Created: 2026-10-08
Epic: voice-agents
Sprint: -

> Cancelled with the pivot recorded in `.procoder/ask/decisions.md`: voice agents leave Hello and become a separate product that connects through SIP trunks. History kept.

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceRuntimeAPI` in `internal/api` passes. It fails if a token without `voice-runtime` reads it, a `voice-runtime` token reads anything else, OAuth consent can grant the scope, the response lacks an unsealed credential or an ETag, `If-None-Match` does not answer `304`, a long poll does not return within `wait` or on a revision change within 2 s, a disabled agent appears, or the operation is reachable through MCP, the assistant or a proposal.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
