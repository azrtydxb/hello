# `.github/workflows/ci.yaml` runs on pull_request and push to main and executes `go test -race ./...` — fails if a red test leaves the job green (verified by `procoder ci`).

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Every change must be gated by CI so regressions are caught before merge.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `.github/workflows/ci.yaml` runs on pull_request and push to main and executes `go test -race ./...` — fails if a red test leaves the job green (verified by `procoder ci`).

## Evidence

Fingerprint: sha256:f5a51a6406c394dc3959da9dd344e030072c86ff54dd22a991b27bdccb952554
Produced: 39 bytes, exit 0
Command: procoder ci

Fingerprint: sha256:3d2f7d1d304909a0e7c02076f6cf71f640e47bab5744b31377cee3fc2daba77a
Produced: 915 bytes, exit 0
Command: gh run view 36981772528 --exit-status
