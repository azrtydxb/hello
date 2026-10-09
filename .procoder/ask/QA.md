# Questions procoder cannot answer for you

Written 2026-10-09 07:42 UTC.

Answer each one by writing a line beginning `Answer: ` under it, then
hand the file back with `procoder ask --file .procoder/ask/QA.md`.
Leave the `Key:` lines alone — they are what ties an answer to its question.

## Q1: [decision] decisions.md

Key: 70cb4a126627
Question: AI integration (2026-10-07)

- Order: phase 1 (OpenAPI, MCP server, agent skills), then phase 2 (in-product AI agent), then phase 3 (voice agents)
- External agent auth: full OAuth 2.1 now; Hello is the authorization server for MCP clients, with protected resource metadata, PKCE and client ID metadata documents
- MCP writes: governed by token scope; write tools only for write-scoped clients, and every call is audited like a user call
- MCP protocol: spec 2026-07-28 (falling back to 2025-11-25), official Go SDK github.com/modelcontextprotocol/go-sdk v1.8.x, stateless Streamable HTTP
- In-product agent: suggest-only like Nexora; every change is a proposal with a before/after diff; an operator applies it, replayed through the API with their permissions
- LLM: configurable endpoints (OpenAI-compatible or Anthropic), private by default; kw uses the local fastllm for now
- All AI and LLM connections use our go-ai-sdk (github.com/azrtydxb/go-ai-sdk), never another client library
- talking-agent stays a separate service and repo; Hello routes calls to it over SIP; it is changed there (personas from Hello, MCP client, auth)
- TTS: keep Breeze TTS 2 for the lab (research-only weights); make TTS pluggable for a commercial model later

Answer: As recorded — this is the decision of 2026-10-07, already in decisions.md; recording it here confirms it (phases 1 and 2 stay, phase 3 pivot recorded separately).

## Q2: [decision] decisions.md

Key: cbda37117ab2
Question: Pivot: voice agents out of Hello (2026-10-09)

- The call/voice agents leave Hello and become a separate product; Hello talks to it only via SIP trunks.
- The built-in AI agent (phase 2: assistant, proposals, detectors) STAYS in Hello, as does phase 1 external access (MCP/OAuth/skills) and per-call quality in CDRs.
- The voice-agent feature is removed from Hello (registry, runtime API, call leg, voice_agent routing/console, voice tables). PR #65 (voice e2e) is closed unmerged.
- New requirement instead: SIP trunks must be able to reach internal numbers (inbound trunk calls targeting internal extensions), not only the outside world.

Answer: As recorded — the pivot of 2026-10-09 stands; this spec (trunk-internal-numbers) is the new requirement it created.

## Q3: [decision] decisions.md

## Q3: [decision] decisions.md

Key: bb1f0088a82e
Question: Provisioning contract 4: MarkFetched and PromoteToken race a rotation or re-arm

Found in the Task 3 pre-PR review (prov-control). Both update by phone id
only, so a fetch that passed PhoneByToken just before a re-arm can set
boot_armed = FALSE after it (undoing the re-arm), and a PromoteToken that
lands after a concurrent rotate-token clears the token the phone is still
using. Fixing it changes the fixed prov.Store contract (Task 1).

- Pass the matched token hash to MarkFetched and PromoteToken and condition the updates on it (contract change; Task 2 and Task 3 adapt)
- Accept the race as documented (an administrator re-arms or rotates again)

Answer: The contract change — pass the matched token hash and condition the updates on it. That is what the merged prov-control code implements (`MarkFetched(ctx, id, hash, st)`, `PromoteToken(ctx, id, hash)` in `internal/prov/store.go`).

## Q4: [spec] trunk-internal-numbers

## Q4: [spec] trunk-internal-numbers

Key: 3312b6690483
Question: **Do the product's own identifiers need to reach the phones?** A peer that calls with no caller ID is presented as `<trunk-name>` (S-4). If the phones should instead see the product's per-call agent identity, that is a header-passing feature this spec does not cover.

Answer: The peer identifies itself per call: the trunk's per-call name/number is presented to the phones, and the received → trunk default → `<trunk-name>` ladder of S-4 stays exactly as the fallback. No header-passing feature in this spec.

## Q5: [spec] trunk-internal-numbers

## Q5: [spec] trunk-internal-numbers

Key: 1a00991f2313
Question: **How does the consuming product signal?** The spec assumes IP-authenticated trunking with pinned source CIDRs (what exists; inbound digest for trunk peers stays out of scope). If the product cannot pin IPs — it runs in the same cluster but its egress addresses may not be static — Hello needs inbound digest registration for trunk peers, which is a phase-2 out-of-scope item and a materially larger change. Answer needed before the plan is tasked.

Answer: Pinned IPs — the product's signalling IPs go into the trunk's source CIDR allowlist, exactly as the spec assumes. Digest REGISTER for trunk peers is out of scope (a possible later milestone).

## Q6: [spec] trunk-internal-numbers

## Q6: [spec] trunk-internal-numbers

Key: 102d6344a840
Question: **Is a `486`/`404` distinction visible enough for the product?** An unallowed dial gets 404 like any unmatched DID; the product may prefer 403 to distinguish policy from numbering. Traces tell Hello's side; the wire code choice is open.

Answer: 404 Not Found for out-of-policy dials, exactly as the spec is written — no 403 for policy, and traces remain Hello's side of the story.

## Q7: [spec] trunk-internal-numbers

## Q7: [spec] trunk-internal-numbers

Key: 1ab52c582e7f
Question: **Should extensions be dialable from trunks under their external numbers too** (a DID map inside the `internal` lookup), or is the extension number itself the contract with the product? The spec takes the second: one route, extension numbers only.

Answer: The extension number is the contract: the peer dials `sip:101@hello` and the number is matched against the trunk's allowed patterns; no DID-to-extension map.
