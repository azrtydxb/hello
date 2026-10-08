# TestKwSmokeVoice (HELLO_KW_SMOKE=1, against the real talking-agent on kw after its changes land) passes. It fails if a call to a test agent through a kw DID does not answer with the greeting, call one attached MCP tool, appear in the CDRs with its summary, and end cleanly.

Status: open
Created: 2026-10-08
Epic: voice-agents
Sprint: -

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

- [ ] [S-33] `TestKwSmokeVoice` (`HELLO_KW_SMOKE=1`, against the real talking-agent on kw after its changes land) passes. It fails if a call to a test agent through a kw DID does not answer with the greeting, call one attached MCP tool, appear in the CDRs with its summary, and end cleanly.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
