# Questions procoder cannot answer for you

Written 2026-10-08 14:27 UTC.

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

## Q3: [spec] voice-agents

Key: e7214583272b
Question: 1. **Who is the audience of "tenant"?** Is talking-agent shared by several Hello instances (lab, kw, perhaps a customer), or only by Hello on kw plus its own web UI? This spec supports many Hello tenants with a per-tenant secret and service account; if there is only one, S-16's key ids and the tenant header can collapse to one secret and one account. (Assumed: many, because the decision calls it multi-tenant.)

Answer: One shared talking-agent for several Hello instances, tenant-scoped (per-tenant secret and service account). Transcripts off by default, opt-in per agent, 30-day retention.

## Q4: [spec] voice-agents

Key: 48b95c74d6ea
Question: 2. **Transcripts.** Default is summaries only, transcripts off per agent and readable by admins only, 30 days. Is that right for your use, and do you need transcripts at all (recorded-calls law differs by country, and an LLM summary is itself derived personal data)?

Answer: Transcripts off by default, opt-in per agent, admin-read only, 30-day retention (see Q3 answer).

## Q5: [spec] voice-agents

Key: 226906a02b58
Question: 3. **MCP credentials beyond bearer/header.** Real MCP servers use OAuth 2.1. Is a static token or header enough for the servers you will attach (for example Hello's own MCP server, home-automation, calendar), or must a voice agent act through OAuth on behalf of a user (a much bigger design: token refresh, per-user consent, caller identity)?

Answer: Both: static bearer/header tokens and OAuth 2.1 client-credentials (for example a Hello service account), all credentials sealed in Hello.

## Q6: [spec] voice-agents

Key: a9850601d3c6
Question: 4. **Caller identity and tool authority.** Today a persona runs with the tools' credentials regardless of who calls (caller ID is never identity). Do you want any caller verification (spoken PIN, known-caller list per agent) before mutating tools, or is "confirm aloud" the whole control?

Answer: talking-agent gets the same licence as Hello (Apache License 2.0, see Hello's LICENSE).

## Q7: [spec] voice-agents

Key: 67129a37407e
Question: 5. **TLS/SRTP between Hello and talking-agent.** Both sit on the same LAN on kw. Is plaintext SIP/RTP acceptable on kw for phase 3 with the HMAC signature (S-16), or is SIPS/SRTP a requirement before first use?

Answer: Spoken confirmation PLUS caller verification (known caller ID / allowlist, or a PIN) before any data-changing tool; tools allowlisted per agent.

## Q8: [spec] voice-agents

Key: 2567471bf9c0
Question: 6. **Licence of talking-agent and Breeze.** `LICENSE.md` contains only `ok`; Breeze TTS 2 weights are research-only. Which licence should talking-agent have, and is any commercial or customer use of the lab voice intended before a commercial TTS is chosen?

Answer: Plaintext SIP/RTP with the HMAC-signed INVITE is acceptable on kw; TLS/SRTP is a later option.

## Q9: [spec] voice-agents

Key: 0dbd568da15e
Question: 7. **Agents in ring groups.** The spec forbids an agent in `ring-all`/`longest-idle`/`weighted` groups and recommends it as the failure target. Do you also want it as a normal `sequential` member (for example "ring the desk phone, then the assistant"), or only as a failure target?

Answer: Sequential member and failure target both allowed (sequential and round-robin as in the spec).

## Q10: [spec] voice-agents

Key: 61c77b143f82
Question: 8. **Test call.** The spec offers a test extension and CDR following, not a browser or console-originated call. Do you want Hello to originate a call to a chosen phone and connect it to the agent (needs click-to-call, which Hello does not have), or a WebRTC softphone in the console (a new media stack)?

Answer: A dialable test extension per agent plus CDR following; no WebRTC and no click-to-call.

## Q11: [spec] voice-agents

Key: 457b17a5bdc0
Question: 9. **Where limits are enforced.** Concurrency is enforced by talking-agent (`486`). Should Hello also cap simultaneous agent calls per agent or globally (it knows the calls and could refuse before sending an INVITE), accepting that two sources then hold the number?

Answer: Hello caps calls per agent and in total (busy sends the call to the failover destination); talking-agent also refuses with 486 when its speech models are saturated.
