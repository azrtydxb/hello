# `TestPhoneDeviceSecretSealed` in `internal/api` passes. It fails if binding a device does not rotate and seal its secret and update its HA1 values in one transaction, if the sealed secret is returned by any endpoint, or if unbinding leaves `secret_enc` set.

Status: done 2026-10-14
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestPhoneDeviceSecretSealed` in `internal/api` passes. It fails if binding a device does not rotate and seal its secret and update its HA1 values in one transaction, if the sealed secret is returned by any endpoint, or if unbinding leaves `secret_enc` set.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/api/prov_test.go:180 TestPhoneDeviceSecretSealed asserts bind rotates and seals the secret (AAD-bound), HA1 matches the sealed secret, no endpoint returns it, unbind clears secret_enc. Green on main CI run 38615122909, go job (database env set).
- Per-condition audit 2026-10-15: bind does not rotate → internal/api/prov_test.go:200-202; not sealed → internal/api/prov_test.go:197-202, :208-210 (AAD-bound); HA1 not updated → internal/api/prov_test.go:204-206; not one transaction → internal/api/prov_test.go:250-266 (gap fixed: nothing asserted atomicity; a bind that fails on the phones_model_check CHECK after the seal must leave the device's HA1, NULL secret_enc and audit rows unchanged); sealed secret returned by an endpoint → internal/api/prov_test.go:212-226 (gap fixed: now also the binding answer and the base64 sealed bytes, over phones, devices, fetches and extensions); unbind leaves secret_enc → internal/api/prov_test.go:244-248.
