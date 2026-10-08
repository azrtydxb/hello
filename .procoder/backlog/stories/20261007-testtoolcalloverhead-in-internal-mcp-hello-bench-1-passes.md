# `TestToolCallOverhead` in `internal/mcp` (`HELLO_BENCH=1`) passes. It fails if a tool call's p99 latency exceeds the same direct API call's p99 by 2 ms or more.

Status: done 2026-10-07
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

<!-- The user story: who needs what, and why. What "done" looks like in
     the reader's terms — a title is not a description. -->

Operators get MCP tool calls that cost no more than the API call they replay: the in-process replay adds under 2 ms at p99 (spec S-15 performance constraint; plan Task 5).

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestToolCallOverhead` in `internal/mcp` (`HELLO_BENCH=1`) passes. It fails if a tool call's p99 latency exceeds the same direct API call's p99 by 2 ms or more.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the story open. -->

Fingerprint: sha256:fceb6f8a05bf102210a1222880b30648b1ab9d84da19c0c07015ac634ef47b89
Produced: 190 bytes, exit 0
Command: env HELLO_BENCH=1 go test -count=1 -run ^TestToolCallOverhead$ -v ./internal/mcp/
