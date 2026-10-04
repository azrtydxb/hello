# `TestVoicemailRetrieval` passes — fails if `*97` without a correct password does not prompt, messages are not listed/played/heard/deleted end to end (API + MinIO object removal).

Status: done 2026-10-04
Created: 2026-10-04
Epic: pbx-features
Sprint: -

## Description

Phase 4 deliverable; see .procoder/specs/pbx-features.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestVoicemailRetrieval` passes — fails if `*97` without a correct password does not prompt, messages are not listed/played/heard/deleted end to end (API + MinIO object removal).

## Evidence

Fingerprint: sha256:ci-run-37215897405-phase-4 (PR: go+web+minio integration green) and main run (publish: 4 images; lab: failure suite green); rollout verified on kw — Application Healthy/Synced at 290e473, 13/13 pods Ready, minio bucket verified, dispatcher shows both sip nodes.
Commands: `gh run view 37215897405`, `gh run view` (main, post-merge), `kubectl get application -n hello`, rollout commits 9832a84/492055b/290e473.
