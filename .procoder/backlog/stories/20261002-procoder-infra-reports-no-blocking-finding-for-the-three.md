# `procoder infra` reports no blocking finding for the three Dockerfiles and `TestImagesNonRoot` in `test/integration` (gated by `HELLO_DOCKER=1`) passes — fails if any image builds without a non-root `USER`.

Status: open
Created: 2026-10-02
Epic: foundation
Sprint: -

## Description

Container images must be buildable and run as non-root for every service.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `procoder infra` reports no blocking finding for the three Dockerfiles and `TestImagesNonRoot` in `test/integration` (gated by `HELLO_DOCKER=1`) passes — fails if any image builds without a non-root `USER`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
