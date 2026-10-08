# TestVoiceMCPDiscovery in internal/voice passes against a fake MCP server (test/fakemcp). It fails if a public or rebinding address is dialled without the opt-in, a redirect is followed, a response over 1 MiB is read, discovery saves anything, or the check result stores a response body.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-7] [S-8] [S-9] `TestVoiceMCPDiscovery` in `internal/voice` passes against a fake MCP server (`test/fakemcp`). It fails if a public or rebinding address is dialled without the opt-in, a redirect is followed, a response over 1 MiB is read, discovery saves anything, or the check result stores a response body.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
