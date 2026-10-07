# `TestKwProvisioningIngress` in `test/deploy` passes. It fails if the kw manifest lacks the `prov.hello.kw.watteel.lab` Ingress, its `Certificate` from `cluster-ca`, `ssl-redirect: "false"`, `enable-access-log: "false"`, or the `hello-prov` Service on port 8083.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKwProvisioningIngress` in `test/deploy` passes. It fails if the kw manifest lacks the `prov.hello.kw.watteel.lab` Ingress, its `Certificate` from `cluster-ca`, `ssl-redirect: "false"`, `enable-access-log: "false"`, or the `hello-prov` Service on port 8083.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:7523551504d4fd16a9aa416f801ef32c3f45eb4a76c5244de3ccb76fbf02bbd5
Produced: 50 bytes, exit 0
Command: go test -race -count=1 -run ^TestKwProvisioningIngress$ ./test/deploy/
