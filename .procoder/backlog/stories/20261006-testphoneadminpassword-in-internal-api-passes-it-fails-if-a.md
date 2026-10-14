# `TestPhoneAdminPassword` in `internal/api` passes. It fails if a new phone has no sealed random admin password, if any built-in template does not set it, if it appears on the boot path, in a preview, in a list or get response or in a log, or if a reveal does not write an audit row.

Status: done 2026-10-14
Created: 2026-10-06
Epic: phone-auto-provisioning-service
Sprint: -

## Description

Phone auto-provisioning deliverable; see .procoder/specs/phone-auto-provisioning-service.md and .procoder/plans/phone-auto-provisioning-service.md.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestPhoneAdminPassword` in `internal/api` passes. It fails if a new phone has no sealed random admin password, if any built-in template does not set it, if it appears on the boot path, in a preview, in a list or get response or in a log, or if a reveal does not write an audit row.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

- internal/api/prov_test.go:289 TestPhoneAdminPassword asserts a sealed random 20-char password per phone, absent from get/list and from a preview, an audit row per reveal, and every built-in template setting it; boot path covered by test/integration/lab_prov_test.go:112 and internal/prov/builtin.go boot bodies. Go job of the PR's CI run runs it with the database env set; the pre-existing assertions are green on main CI run 38615122909.
- Per-condition audit 2026-10-15: no sealed random password → internal/api/prov_test.go:299-311; a built-in does not set it → internal/api/prov_test.go:333-354 (gap fixed: was a substring check of the template source, vacuous on an empty list; now each of the >=5 built-ins is rendered and must output the password); on the boot path → internal/api/prov_test.go:365-385 (gap fixed: was only in test/integration and the fake-store handler test; now the real prov handler on this database serves the yealink common body and the armed phone's hand-off, neither carrying the current or old password); in a preview → internal/api/prov_test.go:356-363; in a list or get → internal/api/prov_test.go:312-316; in a log → internal/api/prov_test.go:387-391 (gap fixed: unasserted; the API and the prov handler now log at debug into a buffer that must not hold either password); reveal without an audit row → internal/api/prov_test.go:317-330.
