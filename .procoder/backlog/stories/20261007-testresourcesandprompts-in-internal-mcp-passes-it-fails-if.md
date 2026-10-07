# `TestResourcesAndPrompts` in `internal/mcp` passes. It fails if a listed resource or template does not return its `GET` operation's body, works without `read`, or a prompt names a tool or resource that does not exist.

Status: done 2026-10-07
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

An agent reads live calls, registrations, recent CDRs, trunk status and the cluster as hello:// resources replayed through the API with its own credentials, and starts from the troubleshoot-call, onboard-user and review-routing prompts, which name only tools and resources that exist (spec S-16; plan Task 5).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestResourcesAndPrompts` in `internal/mcp` passes. It fails if a listed resource or template does not return its `GET` operation's body, works without `read`, or a prompt names a tool or resource that does not exist.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:2ada00ed2e4bb86ac781c194a2139941a8f5f810551c68884affca79a2ee0265
Produced: 436 bytes, exit 0
Command: go test -race -count=1 -run ^TestResourcesAndPrompts$ -v ./internal/mcp/
