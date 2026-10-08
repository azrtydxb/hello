# Voice agents

Status: open
Created: 2026-10-08
Milestone: voice-agents
Spec: voice-agents @ pending (the spec is a draft: its nine open questions are unanswered, so `procoder backlog seed` refuses it; these stories were written by hand from its acceptance criteria and must be reseeded or reconciled once the spec is approved)

## Description

The voice-agent registry in hello-control (personas, versions, limits, MCP servers with sealed credentials and tool allowlists), the `voice_agent` destination in routes, ring groups and extensions, the signed call leg to talking-agent, the runtime API it reads (ETag and long poll, service account with the `voice-runtime` scope), CDR fields and call reports, the console pages, metrics, kw deployment and a fake talking-agent for the lab. The changes talking-agent itself needs are listed in the spec and tracked in its own repository.
