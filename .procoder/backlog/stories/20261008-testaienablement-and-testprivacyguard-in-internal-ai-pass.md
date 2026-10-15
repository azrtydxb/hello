# `TestAIEnablement` in `internal/ai` and `internal/api` and `TestPrivacyGuard` in `internal/ai` pass. They fail if AI turns on without both base URL and model, an AI operation answers other than `503` `ai_disabled` while off, a public or rebinding host is dialled without `HELLO_AI_ALLOW_PUBLIC_ENDPOINT`, the provider is built outside `provider.go`, or the API key appears in status, logs or metrics.

Status: done 2026-10-14
Created: 2026-10-08
Epic: ai-agent
Sprint: -

## Description

An admin needs AI to stay off until it is fully configured, to answer clearly while off, and to never send data to a public endpoint unless explicitly allowed (spec S-1 to S-3). Done when enablement, the 503 ai_disabled contract, the private-endpoint guard and key secrecy are tested.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestAIEnablement` in `internal/ai` and `internal/api` and `TestPrivacyGuard` in `internal/ai` pass. They fail if AI turns on without both base URL and model, an AI operation answers other than `503` `ai_disabled` while off, a public or rebinding host is dialled without `HELLO_AI_ALLOW_PUBLIC_ENDPOINT`, the provider is built outside `provider.go`, or the API key appears in status, logs or metrics.

## Evidence

- `TestAIEnablement` (internal/ai/privacy_test.go:222: on only with base URL and model, nothing starts while off, key absent from status/logs/metrics; internal/api/ai_status_test.go:51: every AI operation but status/settings answers 503 ai_disabled while off) and `TestPrivacyGuard` (internal/ai/privacy_test.go:86: public, mixed, mapped and rebinding hosts refused without HELLO_AI_ALLOW_PUBLIC_ENDPOINT, rebinding undialled, go-ai-sdk providers only in provider.go). `ok internal/ai`, `ok internal/api` in main CI run 38615122909 (go + lab green, `gh run view 38615122909`) and PR #90 CI run 38635554426 (go + web + lab green, `gh run view 38635554426`).
- Per-condition audit 2026-10-15: gaps fixed: the story named only internal/ai, but the 503 ai_disabled half is TestAIEnablement in internal/api (text corrected); TestAIEnablement in internal/ai never checked metrics for the key and now gathers the registry and fails on it. Base URL + model, public/rebinding hosts and provider.go were already asserted.
