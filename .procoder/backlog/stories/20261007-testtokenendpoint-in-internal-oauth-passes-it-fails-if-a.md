# `TestTokenEndpoint` in `internal/oauth` passes. It fails if a wrong PKCE verifier, a reused or expired code, a mismatched redirect URI or client is accepted; if a reused code does not revoke the grant's tokens; if a refresh token is not rotated, a spent one does not revoke the grant, or a refresh widens scope; if the idle or absolute refresh limit is not enforced; if a token is accepted outside its audience (an MCP-bound token directly on `/api/v1`), or an MCP-bound token is refused inside a replay; or if responses lack `no-store`.

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

- [ ] `TestTokenEndpoint` in `internal/oauth` passes. It fails if a wrong PKCE verifier, a reused or expired code, a mismatched redirect URI or client is accepted; if a reused code does not revoke the grant's tokens; if a refresh token is not rotated, a spent one does not revoke the grant, or a refresh widens scope; if the idle or absolute refresh limit is not enforced; if a token is accepted outside its audience (an MCP-bound token directly on `/api/v1`), or an MCP-bound token is refused inside a replay; or if responses lack `no-store`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
