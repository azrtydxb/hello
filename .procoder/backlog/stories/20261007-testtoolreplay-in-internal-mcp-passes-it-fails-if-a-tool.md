# `TestToolReplay` in `internal/mcp` passes. It fails if a tool call does not reach the handler with the caller's credentials and the replay marker, an external request carrying any header can pose as a replay, a replay can replay, a mutation's audit row lacks `via`, an API error is not returned as `isError` with code and fields, or any `x-hello-secret` value (device create included) appears in a result.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [ ] `TestToolReplay` in `internal/mcp` passes. It fails if a tool call does not reach the handler with the caller's credentials and the replay marker, an external request carrying any header can pose as a replay, a replay can replay, a mutation's audit row lacks `via`, an API error is not returned as `isError` with code and fields, or any `x-hello-secret` value (device create included) appears in a result.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->
