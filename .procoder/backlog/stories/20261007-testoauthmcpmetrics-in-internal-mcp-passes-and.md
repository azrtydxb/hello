# `TestOAuthMCPMetrics` in `internal/mcp` passes, and `TestNoSecretsInLogs` also covers OAuth and MCP. They fail if a token issue, a token failure, a CIMD fetch, an MCP request or a tool call does not move its metric, or if any access token, refresh token, code, verifier, client secret or withheld value appears in a log line after the end-to-end test.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestOAuthMCPMetrics` in `internal/mcp` passes, and `TestNoSecretsInLogs` also covers OAuth and MCP. They fail if a token issue, a token failure, a CIMD fetch, an MCP request or a tool call does not move its metric, or if any access token, refresh token, code, verifier, client secret or withheld value appears in a log line after the end-to-end test.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
