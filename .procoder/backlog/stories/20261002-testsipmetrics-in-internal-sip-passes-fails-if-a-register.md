# `TestSIPMetrics` in `internal/sip` passes — fails if a REGISTER and a completed call do not move `hello_sip_requests_total`, `hello_sip_responses_total`, `hello_calls_total` and `hello_sip_registrations`.

Status: open
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestSIPMetrics` in `internal/sip` passes — fails if a REGISTER and a completed call do not move `hello_sip_requests_total`, `hello_sip_responses_total`, `hello_calls_total` and `hello_sip_registrations`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
