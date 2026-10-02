# `procoder infra` reports no blocking finding for the three Dockerfiles and `TestImagesNonRoot` in `test/integration` (gated by `HELLO_DOCKER=1`) passes — fails if any image builds without a non-root `USER`.

Status: done 2026-10-02
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Container images must be buildable and run as non-root for every service.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `procoder infra` reports no blocking finding for the three Dockerfiles and `TestImagesNonRoot` in `test/integration` (gated by `HELLO_DOCKER=1`) passes — fails if any image builds without a non-root `USER`.

## Evidence

Fingerprint: sha256:a1cb6bc3f60ece05b274365b01e82eaa86d02c54ace8b36511ea862d928d01cd
Produced: 179 bytes, exit 0
Command: procoder infra

Fingerprint: sha256:c15a844bed1977e17217a1acaff9f06cf302a457482ef8eb01b693708aa200ad
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 go test -count=1 -run TestImagesNonRoot ./test/integration/
