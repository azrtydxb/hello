# `TestKwAIAgent` in `test/deploy` passes. It fails if the kw manifest does not reference Secret `hello-ai` optionally on hello-control or commits a key.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

The kw deployment needs the AI connection settings to come from an optional, SOPS-encrypted Secret so that the cluster runs without AI and no key is ever committed (spec S-27). Done when a manifest test enforces it.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestKwAIAgent` in `test/deploy` passes. It fails if the kw manifest does not reference Secret `hello-ai` optionally on hello-control or commits a key.

## Evidence

- `TestKwAIAgent` (test/deploy/ai_test.go:172): the kw hello-control reads provider, base URL, model and key from Secret hello-ai with optional: true and no literal value, and secret-hello-ai.sops.yaml is SOPS-encrypted (every data value ENC[...]). `ok test/deploy` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: gap fixed: TestKwAIAgent only checked the four data values, so a plaintext stringData or extra data key would pass; every data and stringData value must now be ENC[...]. The optional secretKeyRef on hello-control was already asserted.
