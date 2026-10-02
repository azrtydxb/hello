# `TestCallAcrossNodes` in `test/integration` passes — fails if a UA registered through hello-sip-1 cannot be called by a UA registered through hello-sip-2.

Status: done 2026-10-02
Created: 2026-10-02
Epic: minimum-pbx
Sprint: -

## Description

Phase 1 deliverable; see .procoder/specs/minimum-pbx.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestCallAcrossNodes` in `test/integration` passes — fails if a UA registered through hello-sip-1 cannot be called by a UA registered through hello-sip-2.

## Evidence

Fingerprint: sha256:21e62ca13b209aa0a687aa25f0f2a001b0d56056ec1884e42154d13924ba4ad1
Produced: 56 bytes, exit 0
Command: env HELLO_DOCKER=1 HELLO_LAB_KEEP=1 go test -count=1 -timeout 15m -run ^TestCallAcrossNodes$ ./test/integration/

Fingerprint: sha256:57dd535008fafd75d10a1bf21a8e9787d38af9848f4b351fd7d48164f1a6fa1e
Produced: 51 bytes, exit 0
Command: go test -race -count=1 -run ^(TestEdgeCallAcrossNodes|TestPeerMissRefreshes)$ ./internal/sip/
