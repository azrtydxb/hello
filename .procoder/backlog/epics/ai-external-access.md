# AI external access

Status: open
Created: 2026-10-07
Milestone: ai-external-access
Spec: ai-external-access @ 326b11cde45f

## Description

Three deliverables that together let external agents operate Hello: an OpenAPI document that is complete and kept in sync with the routes in both directions (responses and request bodies conformance-tested, operations described well enough to drive tools); OAuth 2.1 with Hello as the authorization server (authorization code with PKCE, client ID metadata documents, resource indicators, refresh tokens, a consent screen, service accounts with client credentials, scoped and expiring tokens, revocation and audit) while existing API tokens keep working; and an MCP server in hello-control (official Go SDK, stateless Streamable HTTP) whose tools are generated from the OpenAPI document and replayed in-process with the caller's credentials with user roles (viewer, operator, admin) bounding the console, the API, consent and MCP scopes, plus agent skills in the repository, downloadable from the console.

Seeded from .procoder/specs/ai-external-access.md — one story per acceptance criterion.
