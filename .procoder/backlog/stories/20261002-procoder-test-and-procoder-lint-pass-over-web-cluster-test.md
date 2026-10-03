# `procoder test` and `procoder lint` pass over `web/`. `Cluster.test.tsx` fails if a node's state, load, version or revision lag is not shown, or if draining the last READY node doesn't ask for confirmation.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` and `procoder lint` pass over `web/`. `Cluster.test.tsx` fails if a node's state, load, version or revision lag is not shown, or if draining the last READY node doesn't ask for confirmation.

## Evidence

Fingerprint: sha256:8b29c302c3da67e41d59ca90069e370af871ddd53576ae92acbdce63eb913a04
Produced: 80 bytes, exit 0
Command: pnpm --dir web typecheck

Fingerprint: sha256:ab108ee495f0f0280f9dff9674f4772afd586f88b42a1538421f0e70b2de99f6
Produced: 71 bytes, exit 0
Command: pnpm --dir web lint

Fingerprint: sha256:008455032d8944f2d418cd6452bab911a246bbb1e67c5fd0d043ba6713cff659
Produced: 587 bytes, exit 0
Command: pnpm --dir web test

Fingerprint: sha256:ba9566b603a3d6d70a15010c7398865ab3f82ec233ae796dceb28dc593e8f50b
Produced: 442 bytes, exit 0
Command: pnpm --dir web build
