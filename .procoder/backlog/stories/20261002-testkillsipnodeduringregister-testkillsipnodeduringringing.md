# `TestKillSIPNodeDuringRegister`, `TestKillSIPNodeDuringRinging` and `TestKillSIPNodeDuringCall` in `test/failure` pass. Each fails if, after the kill, a new REGISTER and a new call through Kamailio do not succeed within 20s. They also fail if a ringing caller is left hanging rather than getting a final response or timing out, or if the dead node's call is still listed after 40s.

Status: done 2026-10-03
Created: 2026-10-02
Epic: ha
Sprint: -

## Description

Phase 3 deliverable; see .procoder/specs/ha.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKillSIPNodeDuringRegister`, `TestKillSIPNodeDuringRinging` and `TestKillSIPNodeDuringCall` in `test/failure` pass. Each fails if, after the kill, a new REGISTER and a new call through Kamailio do not succeed within 20s. They also fail if a ringing caller is left hanging rather than getting a final response or timing out, or if the dead node's call is still listed after 40s.

## Evidence

Fingerprint: sha256:518e6823a62eabc5e657691f77663f0ef6c26efa8d39a97eb72c17a069a55001
Produced: 57 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 30m -run ^(TestKillSIPNodeDuringRegister|TestKillSIPNodeDuringRinging|TestKillSIPNodeDuringCall)$ ./test/integration/
