# `procoder test` and `procoder lint` pass over `web/`. `Trunks.test.tsx` fails if the password is shown after saving. `Routes.test.tsx` fails if reordering does not send the new order, or a server validation error is not shown on its field. `CallDetail.test.tsx` fails if the trace steps are not rendered in order.

Status: done 2026-10-02
Created: 2026-10-02
Epic: trunks-routing
Sprint: -

## Description

Phase 2 deliverable.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder test` and `procoder lint` pass over `web/`. `Trunks.test.tsx` fails if the password is shown after saving. `Routes.test.tsx` fails if reordering does not send the new order, or a server validation error is not shown on its field. `CallDetail.test.tsx` fails if the trace steps are not rendered in order.

## Evidence

Fingerprint: sha256:8b29c302c3da67e41d59ca90069e370af871ddd53576ae92acbdce63eb913a04
Produced: 80 bytes, exit 0
Command: pnpm --dir web typecheck

Fingerprint: sha256:ab108ee495f0f0280f9dff9674f4772afd586f88b42a1538421f0e70b2de99f6
Produced: 71 bytes, exit 0
Command: pnpm --dir web lint

Fingerprint: sha256:bb1f3d49e2725aaa5535c507fb5b0564dd506a1ffd0c18a0b9f63294201c6a29
Produced: 587 bytes, exit 0
Command: pnpm --dir web test

Fingerprint: sha256:a94924aebe21c6f1d2b054f476da65e9bf74e2d83c6fb66d54995d04ea0929d8
Produced: 442 bytes, exit 0
Command: pnpm --dir web build
