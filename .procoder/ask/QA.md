# Questions procoder cannot answer for you

Written 2026-10-08 06:03 UTC.

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

Answer:

## Q2: [decision] decisions.md

Key: bb1f0088a82e
Question: Provisioning contract 4: MarkFetched and PromoteToken race a rotation or re-arm

Found in the Task 3 pre-PR review (prov-control). Both update by phone id
only, so a fetch that passed PhoneByToken just before a re-arm can set
boot_armed = FALSE after it (undoing the re-arm), and a PromoteToken that
lands after a concurrent rotate-token clears the token the phone is still
using. Fixing it changes the fixed prov.Store contract (Task 1).

- Pass the matched token hash to MarkFetched and PromoteToken and condition the updates on it (contract change; Task 2 and Task 3 adapt)
- Accept the race as documented (an administrator re-arms or rotates again)

Answer:

## Q3: [spec] ai-agent

Key: 52c1aa98bde5
Question: 1. **Call quality detection.** Hello measures RTP loss and jitter only as per-node Prometheus counters (`hello_rtp_loss_total`, `hello_rtp_jitter_ms`) and only for anchored calls; CDRs carry no quality data and directly-media calls are not measured at all. Options: (a) hello-sip writes per-call loss/jitter summaries into the CDR for anchored calls and a `call_quality` detector flags trunks or nodes with degraded calls; (b) hello-control queries the Prometheus on kw; (c) leave call quality out of phase 2.

Answer:

## Q4: [spec] ai-agent

Key: 1ab11704f3b5
Question: 2. **Public endpoints and personal data.** With `HELLO_AI_ALLOW_PUBLIC_ENDPOINT=true` (for example Anthropic's API), phone numbers, names and User-Agents would leave the network. Should Hello then mask phone numbers and names in `<data>` blocks (consistent tokens per value, unmasked in the console), or send them as is once an administrator opted in?

Answer:

## Q5: [spec] ai-agent

Key: a2698ff8dc49
Question: 3. **Allowlist breadth.** The allowlist (S-11) has no deletes; a config smell such as an unreachable route can only be fixed by editing it. Should phase 2 also allow deleting outbound/inbound routes and ring groups as proposals, or stay update-and-create only?

Answer:
