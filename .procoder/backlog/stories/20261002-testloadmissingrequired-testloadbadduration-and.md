# `TestLoadMissingRequired`, `TestLoadBadDuration` and `TestLoadUnspecifiedAdvertise` in `internal/config` pass — fails if a missing key, a malformed duration, or bind `0.0.0.0` without an advertised address is accepted, or the error omits the key name.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Operators need misconfiguration to fail loudly at startup, naming the key, instead of a node half-starting; bind vs advertised SIP address must be explicit (spec §18).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLoadMissingRequired`, `TestLoadBadDuration` and `TestLoadUnspecifiedAdvertise` in `internal/config` pass — fails if a missing key, a malformed duration, or bind `0.0.0.0` without an advertised address is accepted, or the error omits the key name.

## Evidence

Fingerprint: sha256:131c34d3d1c348b92472df31b1b50fd85f3c8c7c669b4b9e68200d297a93a81d
Produced: 54 bytes, exit 0
Command: go test -race -count=1 ./internal/config/
