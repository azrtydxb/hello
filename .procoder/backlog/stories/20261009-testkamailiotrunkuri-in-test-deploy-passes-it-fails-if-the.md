# `TestKamailioTrunkURI` in `test/deploy` passes. It fails if the kamailio configuration rewrites the Request-URI user on the trunk path (drift test against the shipped cfg).

Status: done 2026-10-14
Created: 2026-10-09
Epic: trunk-internal-numbers
Sprint: -

## Description

As an operator, I need Kamailio to leave the Request-URI user untouched on the trunk path, so that the dialed internal number reaches the PBX unchanged.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKamailioTrunkURI` in `test/deploy` passes. It fails if the kamailio configuration rewrites the Request-URI user on the trunk path (drift test against the shipped cfg).

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- test/deploy/kamailio_drift_test.go:163 TestKamailioTrunkURI; passes locally (-v PASS). main CI run 38615122909 go job: `ok  	github.com/azrtydxb/hello/test/deploy	5.146s`.

