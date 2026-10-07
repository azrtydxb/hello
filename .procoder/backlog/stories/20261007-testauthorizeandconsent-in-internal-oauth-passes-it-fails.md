# `TestAuthorizeAndConsent` in `internal/oauth` passes. It fails if a request without S256 PKCE, with an unregistered or inexact redirect URI, or with a foreign `resource` is accepted; if an error before redirect-URI validation redirects; if a CIMD document that redirects, exceeds 64 KiB, times out, resolves to a private address or names another `client_id` is accepted; if approval issues a scope the user narrowed away or outside `GrantableScopes`; if the redirect lacks `iss` or `state`; or if a bearer token can approve.

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

- [ ] `TestAuthorizeAndConsent` in `internal/oauth` passes. It fails if a request without S256 PKCE, with an unregistered or inexact redirect URI, or with a foreign `resource` is accepted; if an error before redirect-URI validation redirects; if a CIMD document that redirects, exceeds 64 KiB, times out, resolves to a private address or names another `client_id` is accepted; if approval issues a scope the user narrowed away or outside `GrantableScopes`; if the redirect lacks `iss` or `state`; or if a bearer token can approve.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
