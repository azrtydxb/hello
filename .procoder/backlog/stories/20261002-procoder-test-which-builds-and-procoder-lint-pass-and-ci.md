# `procoder test` (which builds `./...`) and `procoder lint` pass and CI produces `hello-control` and `hello-sip` binaries — fails if either `cmd/` main does not compile.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Developers need both service binaries to build and lint cleanly so every later phase starts from a green tree.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` (which builds `./...`) and `procoder lint` pass and CI produces `hello-control` and `hello-sip` binaries — fails if either `cmd/` main does not compile.

## Evidence

Fingerprint: sha256:3bb5979709d9dd18c61152a6ea070a497aea9b985f7598aad5fed02119fb82f5
Produced: 36 bytes, exit 0
Command: procoder test

Fingerprint: sha256:e441c0fcc1b4e6b87344c8b12a3619d26af338434a5fb097b7d7b10a736d177f
Produced: 41 bytes, exit 0
Command: procoder lint

Fingerprint: sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Produced: 0 bytes, exit 0
Command: go build ./cmd/hello-control ./cmd/hello-sip
