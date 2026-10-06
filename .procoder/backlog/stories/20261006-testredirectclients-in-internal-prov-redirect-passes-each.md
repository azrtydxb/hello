# `TestRedirectClients` in `internal/prov/redirect` passes. Each vendor client is exercised against an `httptest` server reproducing that vendor's documented API, and the test fails if create, rotate or delete does not issue the documented call with the per-device URL, if a credential appears in a log or error string, if a failure does not back off and surface `failed`, or if no credentials means any call is made.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-11); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md. Depends on the spec's open question 3 (vendor redirect accounts) for which clients are live-tested: build the answer-independent part first.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestRedirectClients` in `internal/prov/redirect` passes. Each vendor client is exercised against an `httptest` server reproducing that vendor's documented API, and the test fails if create, rotate or delete does not issue the documented call with the per-device URL, if a credential appears in a log or error string, if a failure does not back off and surface `failed`, or if no credentials means any call is made.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
