# `TestVoiceMetrics` in `internal/voice` and `TestNoSecretsInLogs` cover voice. They fail if a call, setup, unreachable reason, runtime check or MCP check does not move its metric, or a prompt, credential, signature or transcript appears in a log line.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceMetrics` in `internal/voice` and `TestNoSecretsInLogs` cover voice. They fail if a call, setup, unreachable reason, runtime check or MCP check does not move its metric, or a prompt, credential, signature or transcript appears in a log line.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
