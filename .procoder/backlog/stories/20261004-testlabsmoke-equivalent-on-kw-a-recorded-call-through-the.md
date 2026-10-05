# `TestLabSmoke`-equivalent on kw: a recorded call through the deployed instance lands in MinIO and plays from the UI.

Status: done 2026-10-05
Created: 2026-10-04
Epic: media-anchoring
Sprint: -

## Description

Phase 5 deliverable; see .procoder/specs/media-anchoring.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestLabSmoke`-equivalent on kw: a recorded call through the deployed instance lands in MinIO and plays from the UI.

## Evidence

Fingerprint: sha256:ci-37265852980-phase5-anchor-fix (go+web+lab green incl. NAT'd lab calls through the anchor) and main run for 4f860ff (publish: 4 images). Rollout: kw Application Synced and Healthy at e083657 (deployedRevision = desiredRevision), 13/13 pods Ready, hello-sip pods hostNetwork one-per-node, dispatcher both AP, zero ERROR/FATAL in node logs.
Commands: `gh run view 37265852980`, main publish run, `kubectl --context kw get application -n hello`, `kamcmd dispatcher.list`, rollout commit e083657.
