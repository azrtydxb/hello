# `TestProposalValidation` in `internal/ai/proposal` passes. It fails if an operation outside the allowlist, an allowlisted operation that is not `write` or returns a secret, a body with an unknown or credential property, a path parameter naming a missing row, or more than 8 actions is stored. `TestProposalDeleteValidation` passes; it fails if a delete proposal is not visually marked, does not require an explicit confirmation, or fails to show what references the deleted resource in the diff.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An admin needs AI proposals limited to a safe allowlist of write operations with valid bodies, existing targets and no credentials, and deletes clearly marked with what they affect (spec S-11, S-12). Done when validation refuses everything else and delete proposals are marked, confirmed and show their references.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestProposalValidation` in `internal/ai/proposal` passes. It fails if an operation outside the allowlist, an allowlisted operation that is not `write` or returns a secret, a body with an unknown or credential property, a path parameter naming a missing row, or more than 8 actions is stored. `TestProposalDeleteValidation` passes; it fails if a delete proposal is not visually marked, does not require an explicit confirmation, or fails to show what references the deleted resource in the diff.

## Evidence

- `TestProposalValidation` (internal/ai/proposal/validation_test.go:31: CheckDocument proves every allowlisted operation is in the document, write-scoped and secret-free; outside-allowlist, unknown/mistyped/credential bodies, missing or traversal targets and a ninth action refused) and `TestProposalDeleteValidation` (:155: deletes marked destructive with references listed and a removal-only diff). The UI half `TestProposalDeleteUI` in web/src/pages/AIProposals.test.tsx marks the delete, needs the explicit confirmation and now asserts the references list. `ok internal/ai/proposal` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`); web job green in PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
