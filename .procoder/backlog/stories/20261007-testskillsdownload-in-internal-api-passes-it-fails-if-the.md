# `TestSkillsDownload` in `internal/api` passes. It fails if the list differs from the embedded skills, a download is not a zip whose files equal the skill folder, an unknown name is not `404`, or the download works without `read`.

Status: done 2026-10-14
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

An administrator installing a skill in an MCP client downloads it from the console: GET /api/v1/skills lists the embedded skills and GET /api/v1/skills/{name}/download returns the folder as a zip ready to unpack.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestSkillsDownload` in `internal/api` passes. It fails if the list differs from the embedded skills, a download is not a zip whose files equal the skill folder, an unknown name is not `404`, or the download works without `read`.

## Evidence

- `go test -race ./internal/api/ -run TestSkillsDownload` ok (2026-10-08): list equals skills.List(), each zip equals its folder byte for byte, unknown/traversal names 404, no credentials 401, both route rows carry scope read (the 403 for a token without read is TestScopeEnforcement's, Task 3).
- Mutations (snapshot, edit, run, restore, `cmp`): Zip skipping references/ -> FAIL "holds 1 files, the folder 4"; unknown name answered 400 -> FAIL "= 400, want 404".
- Resolved (was open): "works without read" is enforced by auth.Require, a pass-through until Task 3 (ai-oauth); tick once TestScopeEnforcement lands.
- Main CI run 38615122909 (ci.yaml, sha 3861512), go job green: go test -race ./... reported ok for internal/api with HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR set (DB and Valkey tests not skipped); the test at internal/api/skills_test.go:26 TestSkillsDownload asserts each failure the criterion lists.
- Per-condition audit 2026-10-15: list differs -> internal/api/skills_test.go DeepEqual against skills.List(); zip differs from the folder -> byte-for-byte compare; unknown name not 404 -> unknown/traversal loop; works without read -> the noread token must get 403 insufficient_scope (enforced now; the Open note above is resolved). All asserted; no change.
