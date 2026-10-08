# `TestVoiceCallerVerification` in `internal/api` and `internal/voice` passes. It fails if a write tool attaches to an agent with mode `none`, a PIN is stored or returned unhashed, a trunk caller on the allowlist counts as verified by number, the allowlist or PIN hash reaches an MCP tool or audit text, or the runtime view lacks them.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceCallerVerification` in `internal/api` and `internal/voice` passes. It fails if a write tool attaches to an agent with mode `none`, a PIN is stored or returned unhashed, a trunk caller on the allowlist counts as verified by number, the allowlist or PIN hash reaches an MCP tool or audit text, or the runtime view lacks them.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
