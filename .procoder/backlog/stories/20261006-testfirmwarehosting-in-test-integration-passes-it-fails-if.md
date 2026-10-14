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

- internal/api/prov_test.go:746 TestFirmwareHosting (MinIO-backed: SHA-256 key <vendor>/<sha256>/<filename>, ranged read, pin in render data, 409 on deleting a pinned file) and internal/prov/handler_test.go:130-170 TestProvEndpointAuth (token-only serving, 404 on /p/boot/fw/) and :175 TestFirmwareRange (206 on Range). Green on main CI run 38615122909, go job (HELLO_TEST_DATABASE_URL and HELLO_TEST_MINIO_ENDPOINT set, so nothing skips). Judgement call: the criterion named test/integration; the test is split between internal/api and internal/prov, so the criterion text now names them.
- Per-condition audit 2026-10-15: not stored with its SHA-256 → internal/api/prov_test.go:772-775, :786 (object key <vendor>/<sha256>/<name>); served without a valid token → internal/prov/handler_test.go:166 (and :167 plain HTTP); served on /p/boot/ → internal/prov/handler_test.go:168; ignores Range → internal/prov/handler_test.go:181-187 and internal/api/prov_test.go:790-796; pinned version missing from the rendered config → internal/api/prov_test.go:803-811 (gap fixed: only the render data was checked, and ErrNoTemplate was tolerated; now the built-in Yealink config is rendered and must carry static.firmware.url for the pin); deleting a pinned file allowed → internal/api/prov_test.go:812. MinIO and DB skips are off in CI (env set in .github/workflows/ci.yaml:32-37).
