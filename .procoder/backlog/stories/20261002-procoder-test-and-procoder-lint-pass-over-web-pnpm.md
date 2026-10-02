# `procoder test` and `procoder lint` pass over `web/` (pnpm typecheck, lint, build, vitest); `Dashboard.test.tsx` fails if the Dashboard does not render the version returned by a mocked `/api/v1/version`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Administrators need a UI shell with the spec §21 navigation that already talks to the API, so features land into an existing frame.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` and `procoder lint` pass over `web/` (pnpm typecheck, lint, build, vitest); `Dashboard.test.tsx` fails if the Dashboard does not render the version returned by a mocked `/api/v1/version`.

## Evidence

Fingerprint: sha256:8b29c302c3da67e41d59ca90069e370af871ddd53576ae92acbdce63eb913a04
Produced: 80 bytes, exit 0
Command: pnpm --dir web typecheck

Fingerprint: sha256:ab108ee495f0f0280f9dff9674f4772afd586f88b42a1538421f0e70b2de99f6
Produced: 71 bytes, exit 0
Command: pnpm --dir web lint

Fingerprint: sha256:82d33b5a28537bb01336f24de33462a97c6f1bf42eb8ac11260c7b6464a92469
Produced: 284 bytes, exit 0
Command: pnpm --dir web test

Fingerprint: sha256:60d6a9b1bdab5c40101fb27be2375c2c4c4ce5bab19cbd8e0e9511d9802326f6
Produced: 438 bytes, exit 0
Command: pnpm --dir web build
