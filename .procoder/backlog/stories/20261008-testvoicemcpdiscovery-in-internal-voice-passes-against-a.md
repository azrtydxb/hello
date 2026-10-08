# `TestVoiceMCPDiscovery` in `internal/voice` passes against a fake MCP server (`test/fakemcp`, with a fake token endpoint for client credentials). It fails if an OAuth exchange is not made for a server with that auth, a token is stored or logged, a public or rebinding address is dialled without the opt-in, a redirect is followed, a response over 1 MiB is read, discovery saves anything, or the check result stores a response body.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceMCPDiscovery` in `internal/voice` passes against a fake MCP server (`test/fakemcp`, with a fake token endpoint for client credentials). It fails if an OAuth exchange is not made for a server with that auth, a token is stored or logged, a public or rebinding address is dialled without the opt-in, a redirect is followed, a response over 1 MiB is read, discovery saves anything, or the check result stores a response body.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
