# `TestVoiceMCPServers` in `internal/api` and `internal/store` passes (bearer, header and `oauth_client_credentials`, whose client secret is sealed and write-only like any credential, and whose token URL obeys the egress guard). It fails if a credential is returned by any operation other than the runtime one, is stored unsealed, is changed by a PUT without one, if an operator can set one, if an empty allowlist attaches a tool, if a read-only-hinted tool defaults to `confirm` or an unannotated one does not, or if two attached tools collide.

Status: cancelled 2026-10-09 — the voice-agent feature left Hello (decision "Pivot: voice agents out of Hello", .procoder/ask/decisions.md)
Created: 2026-10-08
Epic: voice-agents
Sprint: -

> Cancelled with the pivot recorded in `.procoder/ask/decisions.md`: voice agents leave Hello and become a separate product that connects through SIP trunks. History kept.

## Description

Implements the acceptance criterion of the voice-agents spec (`.procoder/specs/voice-agents.md`) cited below; the plan task that owns it is in `.procoder/plans/voice-agents.md`.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestVoiceMCPServers` in `internal/api` and `internal/store` passes (bearer, header and `oauth_client_credentials`, whose client secret is sealed and write-only like any credential, and whose token URL obeys the egress guard). It fails if a credential is returned by any operation other than the runtime one, is stored unsealed, is changed by a PUT without one, if an operator can set one, if an empty allowlist attaches a tool, if a read-only-hinted tool defaults to `confirm` or an unannotated one does not, or if two attached tools collide.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
