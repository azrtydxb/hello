# `TestLoadMissingRequired`, `TestLoadBadDuration` and `TestLoadUnspecifiedAdvertise` in `internal/config` pass — fails if a missing key, a malformed duration, or bind `0.0.0.0` without an advertised address is accepted, or the error omits the key name.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Operators need misconfiguration to fail loudly at startup, naming the key, instead of a node half-starting; bind vs advertised SIP address must be explicit (spec §18).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestLoadMissingRequired`, `TestLoadBadDuration` and `TestLoadUnspecifiedAdvertise` in `internal/config` pass — fails if a missing key, a malformed duration, or bind `0.0.0.0` without an advertised address is accepted, or the error omits the key name.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
