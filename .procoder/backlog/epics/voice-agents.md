# voice-agents

Status: cancelled 2026-10-09 — the voice-agent feature left Hello (decision "Pivot: voice agents out of Hello", .procoder/ask/decisions.md)
Created: 2026-10-08
Spec: voice-agents @ e18cc784f3b3

> Cancelled with the pivot recorded in `.procoder/ask/decisions.md`: voice agents leave Hello and become a separate product that connects through SIP trunks. History kept.

## Description

<!-- What this epic delivers and why it is one coherent unit of value.
     Stories reference this epic by its file name. -->

The voice-agent registry in hello-control (personas, versions, limits, MCP servers with sealed credentials and tool allowlists), the `voice_agent` destination in routes, ring groups and extensions, the signed call leg to talking-agent, the runtime API it reads (ETag and long poll, service account with the `voice-runtime` scope), CDR fields and call reports, the console pages, metrics, kw deployment and a fake talking-agent for the lab. The changes talking-agent itself needs are listed in the spec and tracked in its own repository.

Seeded from .procoder/specs/voice-agents.md, one story per acceptance criterion.
