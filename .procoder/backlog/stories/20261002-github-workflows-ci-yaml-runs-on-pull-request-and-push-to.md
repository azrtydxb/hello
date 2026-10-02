# `.github/workflows/ci.yaml` runs on pull_request and push to main and executes `go test -race ./...` — fails if a red test leaves the job green (verified by `procoder ci`).

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Every change must be gated by CI so regressions are caught before merge.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `.github/workflows/ci.yaml` runs on pull_request and push to main and executes `go test -race ./...` — fails if a red test leaves the job green (verified by `procoder ci`).

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
