# `procoder test` and `procoder lint` pass over `web/`; `Devices.test.tsx` fails if the secret is not shown after creation or is still shown after navigating away; `Login.test.tsx` fails if an unauthenticated API response does not redirect to `/login`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` and `procoder lint` pass over `web/`; `Devices.test.tsx` fails if the secret is not shown after creation or is still shown after navigating away; `Login.test.tsx` fails if an unauthenticated API response does not redirect to `/login`.

## Evidence

Fingerprint: sha256:bd5771662a0a371560c2c927148fe380f7cb6bc6c615e0b7e7939a4c7a6dab77
Produced: 88 bytes, exit 0
Command: pnpm --dir web typecheck

Fingerprint: sha256:437f2afe8e211c1ee5d3b4e0ec792472f7ec78711e95cc9ff68db049341bd0f5
Produced: 79 bytes, exit 0
Command: pnpm --dir web lint

Fingerprint: sha256:6e1f5827ab61e9f19f6986d8ba9680aa946d43f5d1709d53ffb79d6ff08cdb6e
Produced: 302 bytes, exit 0
Command: pnpm --dir web test

Fingerprint: sha256:50f1accf61e10c2f1b9f51445407c133ecff75f761a7be08f8e6537d62c00f87
Produced: 447 bytes, exit 0
Command: pnpm --dir web build
