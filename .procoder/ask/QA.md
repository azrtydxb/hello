# Questions procoder cannot answer for you

Written 2026-10-07 20:23 UTC.

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

Key: 803693c6f34d
Question: AI phase 1: auditing MCP read calls

A user's reads write no audit row today; the decision says every MCP call is audited like a user call (spec ai-external-access, S-15).

- Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)
- Every MCP tool call, reads included, writes an audit row

Answer: Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)

## Q3: [decision] decisions.md

Key: 43202daa3527
Question: AI phase 1: dynamic client registration on kw

Client ID metadata documents are preferred and DCR is deprecated, but some MCP clients still only register dynamically (spec ai-external-access, S-10).

- On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)
- Off: only client ID metadata documents and service accounts

Answer: On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)

## Q4: [decision] decisions.md

Key: 237b6f7b23a7
Question: AI phase 1: user roles for OAuth consent

Hello has one kind of user, who can do everything, so the role that should bound the scopes a user can grant bounds nothing today (spec ai-external-access, S-5).

- Keep one role in phase 1: every user may grant every scope, `secrets` included; `GrantableScopes` is where a later roles spec plugs in (proposed)
- Add roles now (for example administrator, operator, read-only), applied to the console and the API as well as to consent

Answer: Add roles now: viewer (read-only), operator (day-to-day configuration writes), admin (users, roles, tokens, secrets, OAuth clients, settings); existing users become admin; the console shows and edits a user's role; the API enforces a minimum role on every route; consent, MCP scopes and service accounts are bounded by role

## Q5: [decision] decisions.md

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

## Q6: [spec] ai-external-access

Key: 01f93861d03b
Question: OPEN: Auditing MCP reads. "Every call is audited like a user call": a user's reads write no audit row today. Should MCP read tool calls write audit rows too, or is a log line plus metrics per call (and the normal audit rows for every change) enough? Proposed: log line plus metrics for reads, audit rows for changes, as for users.

Answer: Reads get a log line and metrics; every change writes its normal audit row with the client in `via` (proposed)

## Q7: [spec] ai-external-access

Key: 56930f270aca
Question: OPEN: Dynamic client registration on kw. Client ID metadata documents are the preferred registration and DCR is deprecated, but some MCP clients still only register dynamically. Should `HELLO_OAUTH_DCR` be on for kw? Proposed: on, so every client works today, with the rate limit, the "unverified" consent label and the cleanup rules of S-10.

Answer: On for kw, rate-limited, labelled unverified on the consent screen, unused clients cleaned up (proposed)

## Q8: [spec] ai-external-access

Key: b4b673d5b75c
Question: OPEN: User roles. Hello has one kind of user, who can do everything, so "the user's role bounds the scopes on the consent screen" bounds nothing today. Should phase 1 add roles (for example administrator, operator, read-only, applied to the console and the API too), or keep one role so every user can grant every scope including `secrets`? Proposed: keep one role in phase 1 (`GrantableScopes` is the single place a later roles spec plugs in).

Answer: Add roles now: viewer (read-only), operator (day-to-day configuration writes), admin (users, roles, tokens, secrets, OAuth clients, settings); existing users become admin; the console shows and edits a user's role; the API enforces a minimum role on every route; consent, MCP scopes and service accounts are bounded by role
