# `TestOAuthMCPMetrics` in `internal/mcp` passes, and `TestNoSecretsInLogs` also covers OAuth and MCP. They fail if a token issue, a token failure, a CIMD fetch, an MCP request or a tool call does not move its metric, or if any access token, refresh token, code, verifier, client secret or withheld value appears in a log line after the end-to-end test.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Operators see OAuth and MCP activity in hello_oauth_* and hello_mcp_* metrics and a tool-call log line, and no credential or withheld value ever reaches a log (spec S-20; MCP half in plan Task 5).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestOAuthMCPMetrics` in `internal/mcp` passes, and `TestNoSecretsInLogs` also covers OAuth and MCP. They fail if a token issue, a token failure, a CIMD fetch, an MCP request or a tool call does not move its metric, or if any access token, refresh token, code, verifier, client secret or withheld value appears in a log line after the end-to-end test.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:dba4698f6cef4b218d6528bfb2ef882582531fc58692d84d41816f64018c80e0
Produced: 124 bytes, exit 0
Command: go test -race -count=1 -run ^TestOAuthMCPMetrics$ -v ./internal/mcp/

Both halves pass: the OAuth half (client credentials issue, invalid_client failure, client ID metadata document fetch, on the same registry and log as the MCP metrics, no token or client secret in the log) and the MCP half. Open: the `TestNoSecretsInLogs` extension is plan Task 7.
