# `TestSkills` in `skills` passes. It fails if a skill's frontmatter is missing or invalid, its name differs from its folder, `SKILL.md` exceeds 500 lines, a relative link is broken, or a camelCase inline-code identifier is not an exposed MCP tool.

Status: open
Created: 2026-10-07
Epic: ai-external-access
Sprint: -

## Description

Agents that connect to Hello's MCP server need workflow guidance that never drifts from the tools: the three skills (hello-setup, hello-routing, hello-troubleshoot) ship embedded in hello-control, and TestSkills keeps them valid against the tools derived from internal/api/openapi.json.

## Acceptance criteria

<!-- Each criterion is testable. Check a box ONLY when it is verifiably
     true — the closer will ask for the evidence. -->

- [x] `TestSkills` in `skills` passes. It fails if a skill's frontmatter is missing or invalid, its name differs from its folder, `SKILL.md` exceeds 500 lines, a relative link is broken, or a camelCase inline-code identifier is not an exposed MCP tool.

## Evidence

- `go test -race ./skills/` ok (2026-10-08, branch ai-console): TestSkills checks all three skills and, through fstest cases, that the checker reports each fault (missing/invalid frontmatter, name differs, >500 lines, no references/, broken link, unknown camelCase tool).
- Mutation: `testRouting` renamed to `routingTest` in hello-routing/SKILL.md (snapshot, edit, run, restore, `cmp`) -> FAIL "`routingTest` is not a tool the MCP server exposes".
