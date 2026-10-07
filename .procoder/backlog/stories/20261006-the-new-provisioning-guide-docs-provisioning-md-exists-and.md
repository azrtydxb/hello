# The new provisioning guide, docs/provisioning.md, exists and covers every item S-19 lists; `TestDocsProvisioningLinks` in `test/deploy` fails if it is missing a section for a first-class vendor, the DHCP values differ from what `GET /api/v1/prov/settings` computes for the lab, or `docs/phones.md` does not link to it.

Status: done 2026-10-07
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] The new provisioning guide, docs/provisioning.md, exists and covers every item S-19 lists; `TestDocsProvisioningLinks` in `test/deploy` fails if it is missing a section for a first-class vendor, the DHCP values differ from what `GET /api/v1/prov/settings` computes for the lab, or `docs/phones.md` does not link to it.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

== paste this under `## Evidence`:

Fingerprint: sha256:c1de1aa193139cabdc0b8a65386cb94be2928bdf902b35a617232e02bf0bd263
Produced: 50 bytes, exit 0
Command: go test -race -count=1 -run ^TestDocsProvisioningLinks$ ./test/deploy/
