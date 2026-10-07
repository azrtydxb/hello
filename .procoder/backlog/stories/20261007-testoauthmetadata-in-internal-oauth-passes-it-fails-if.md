# `TestOAuthMetadata` in `internal/oauth` passes. It fails if either metadata document lacks a field listed in S-7 or names another issuer or resource, if a `401` from `/mcp` or `/api/v1` lacks `resource_metadata`, or if any OAuth or MCP path answers other than `404` without `HELLO_PUBLIC_URL`.

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

- [ ] `TestOAuthMetadata` in `internal/oauth` passes. It fails if either metadata document lacks a field listed in S-7 or names another issuer or resource, if a `401` from `/mcp` or `/api/v1` lacks `resource_metadata`, or if any OAuth or MCP path answers other than `404` without `HELLO_PUBLIC_URL`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
