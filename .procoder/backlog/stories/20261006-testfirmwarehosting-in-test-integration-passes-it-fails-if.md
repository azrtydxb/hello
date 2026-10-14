# `TestFirmwareHosting` in `internal/api` (with its handler half, `TestProvEndpointAuth` and `TestFirmwareRange` in `internal/prov`) passes. It fails if an uploaded firmware is not stored with its SHA-256, is served without a valid token or on `/p/boot/`, ignores `Range`, or if a pinned version is missing from the rendered config or deleting a pinned file is allowed.

Status: done 2026-10-14
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestFirmwareHosting` in `internal/api` (with its handler half, `TestProvEndpointAuth` and `TestFirmwareRange` in `internal/prov`) passes. It fails if an uploaded firmware is not stored with its SHA-256, is served without a valid token or on `/p/boot/`, ignores `Range`, or if a pinned version is missing from the rendered config or deleting a pinned file is allowed.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/api/prov_test.go:621 TestFirmwareHosting (MinIO-backed: SHA-256 key <vendor>/<sha256>/<filename>, ranged read, pin in render data, 409 on deleting a pinned file) and internal/prov/handler_test.go:130-170 TestProvEndpointAuth (token-only serving, 404 on /p/boot/fw/) and :175 TestFirmwareRange (206 on Range). Green on main CI run 38615122909, go job (HELLO_TEST_DATABASE_URL and HELLO_TEST_MINIO_ENDPOINT set, so nothing skips). Judgement call: the criterion named test/integration; the test is split between internal/api and internal/prov, so the criterion text now names them.
