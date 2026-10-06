# `TestKwProvisioningIngress` in `test/deploy` passes. It fails if the kw manifest lacks the `prov.hello.kw.watteel.lab` Ingress, its `Certificate` from `cluster-ca`, `ssl-redirect: "false"`, `enable-access-log: "false"`, or the `hello-prov` Service on port 8083.

Status: open
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable (S-12); see .procoder/specs/phone-auto-provisioning-service.md and the plan .procoder/plans/phone-auto-provisioning-service.md. Depends on the spec's open question 1 (provisioning host certificate): build the answer-independent part first.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestKwProvisioningIngress` in `test/deploy` passes. It fails if the kw manifest lacks the `prov.hello.kw.watteel.lab` Ingress, its `Certificate` from `cluster-ca`, `ssl-redirect: "false"`, `enable-access-log: "false"`, or the `hello-prov` Service on port 8083.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
