# `TestConfigChangeAuditedAndRevisioned` in `test/integration` passes — fails if creating, updating or deleting an extension does not write an audit row and increase `config_revision` exactly once.

Status: open
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestConfigChangeAuditedAndRevisioned` in `test/integration` passes — fails if creating, updating or deleting an extension does not write an audit row and increase `config_revision` exactly once.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
