# Voice agents

Status: open
Created: 2026-10-08

## Goal

Phase 3 of the AI integration: an operator creates voice agents with different personas in a simple Hello console, attaches different MCP servers to each, and assigns them like a phone: an extension, a DID, a ring group member or last resort, a route destination, a line in the CDRs. Hello routes the call over SIP, signed, to the separate talking-agent service, which reads personas and tools from Hello's runtime API with live reload; Hello's media path is unchanged.
