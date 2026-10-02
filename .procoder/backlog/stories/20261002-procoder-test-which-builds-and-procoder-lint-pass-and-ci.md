# `procoder test` (which builds `./...`) and `procoder lint` pass and CI produces `hello-control` and `hello-sip` binaries — fails if either `cmd/` main does not compile.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Developers need both service binaries to build and lint cleanly so every later phase starts from a green tree.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder test` (which builds `./...`) and `procoder lint` pass and CI produces `hello-control` and `hello-sip` binaries — fails if either `cmd/` main does not compile.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
