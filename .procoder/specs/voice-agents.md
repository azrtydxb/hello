# voice-agents

Status: draft

Source: `.procoder/backlog/milestones/voice-agents.md` and its epic `voice-agents`. Phase 3 of the AI integration decided 2026-10-07 (`.procoder/ask/decisions.md`, "AI integration (2026-10-07)"):

- **talking-agent stays a separate, shared, multi-tenant service and repo** (`github.com/piwi3910/talking-agent`). Hello does not host speech, STT, TTS or an LLM loop. It routes calls to talking-agent over SIP (talking-agent's own sipgo/diago telephony, G.711 PCMU/PCMA at 8 kHz); Hello's media path is unchanged
- **TTS:** Breeze TTS 2 stays for the lab (research-only weights); TTS is pluggable in talking-agent for a commercial model later
- **LLM:** every LLM connection in Hello uses `github.com/azrtydxb/go-ai-sdk`. Hello itself makes no LLM call in this phase (talking-agent does the conversation); Hello's only model-adjacent code is an MCP client for "test connection" (S-9)
- **phases 1 and 2 are live:** OpenAPI with `x-hello-scope`/`x-hello-role`/`x-hello-mcp`/`x-hello-secret` (`internal/apispec`), OAuth 2.1, roles `viewer`/`operator`/`admin`, service accounts, the MCP server, the in-product agent with proposals

Verified against talking-agent at commit `0ba7185` (`deploy/sip/README.md`, `deploy/kw/manifests/sip-service.yaml`, `internal/tools/tools.go`):

- SIP number = exact Request-URI user; unknown number `404`, untrusted peer `403`, capacity `486`
- personas and their numbers live in talking-agent's own `PHONE_SETTINGS_FILE` (JSON on a PVC) edited in its web UI; peers are trusted by `allowed_peers` source IP (plus `SIP_TRUSTED_FORWARDERS` on kw, because Cilium L2 forwarding masks the source)
- kw publishes SIP UDP/TCP 5060 and RTP UDP 10000-10199 on **192.168.10.142** through `talking-agent-sip` (confirmed in `deploy/kw/manifests/sip-service.yaml` annotation `lbipam.cilium.io/ips` and `SIP_ADVERTISE_IP`)
- tools run through `tools.Executor` (`Execute(ctx, Definition, Request, telemetry.Sink) Result`), today an `HTTPExecutor`
- licence file is `LICENSE.md` (see Open questions)

## Problem

Hello routes a call to a person, a ring group or a trunk. An operator who wants a voice assistant today must run talking-agent, open its separate settings UI, type persona numbers there, and add a Hello inbound route with a raw `sip_uri` to it. Two consoles, two sources of truth for numbers, no CDR link between the call and the persona that answered, no way to give each persona its own tools, and nothing in Hello that says an agent exists. The vision is that an operator creates a voice agent with a persona in Hello's console in a few minutes, attaches the MCP servers it may use, and then treats it like any phone: an extension, a DID, a ring group member or last resort, a route destination, a line in the CDRs.

## Users

- **Viewers:** see voice agents, their routes, call history and runtime status; see MCP servers without credentials. Cannot edit.
- **Operators:** create and edit voice agents and personas, attach and detach MCP servers, set tool allowlists, enable and disable agents, route to them, read call summaries.
- **Administrators:** everything operators do, plus create and rotate MCP server credentials, create the runtime service account and the shared call secret, read transcripts when enabled.
- **Callers:** reach an agent by dialling an extension or a DID and are told by the greeting that it is an automated assistant; they hear a clear failure or fall through to the ring group's next step when the agent cannot answer.
- **talking-agent (a machine user):** reads every persona and its tools from Hello with a service account, authenticates calls it receives from Hello, reports finished calls back.
- **Security reviewers:** rely on MCP credentials being sealed at rest and visible only to the runtime service account, on Hello being the only party that can place a call to an agent, and on no agent holding Hello admin rights.

## In scope

### Registry and personas

- [S-1] **Voice agent.** A row in hello-control: `name` (`^[A-Za-z0-9._-]{1,64}$`, unique), `description`, `enabled`, and a persona: `prompt` (system prompt, up to 8000 characters), `greeting` (spoken first, up to 500), `language` (BCP 47, for STT and replies), `voice` (a talking-agent voice id, empty = its default), `voice_reference` (a reference-voice name known to talking-agent, optional, Breeze), `style` (up to 500, persona voice style), `temperature` unset by default. `sip_user` is generated once (`va-<8 hex>`, never edited) and is what Hello puts in the Request-URI; it hides the agent's name and extension from talking-agent's number table. Names, not ids, appear in routes so exports stay readable.
- [S-2] **Limits.** Per agent: `max_call_seconds` (default 600, 30-1800), `max_concurrent` (default 4, 1-50; enforced by talking-agent, `486` when full), `max_tool_calls` per call (default 20), `idle_timeout_seconds` (default 20, hang up after silence). Creating more than `HELLO_VOICE_MAX_AGENTS` (50) answers `409` `voice_agent_limit`.
- [S-3] **Number.** An agent may have an `extension` (an internal number in the same namespace as extensions, unique across both; the extension table's regex applies). Dialling it from any phone reaches the agent. An agent without an extension is reachable only through routes and ring groups.
- [S-4] **Persona versions.** Every change of a persona or an attachment bumps a per-agent `revision` and the global `voice_revision`. The last 10 persona versions per agent are kept (`voice_agent_versions`, who and when); an operator can restore one (a new revision, not a rewrite of history). Hello's audit row names the field names changed, never the prompt text.

### MCP servers and tools

- [S-5] **MCP server registry.** `voice_mcp_servers`: `name` (unique), `url` (`https://` or `http://`; plain `http` only to private addresses), `auth` (`none`, `bearer`, `header`), the credential (token, or header name plus value) **sealed** with `internal/secret` (additional data `voice_mcp:<id>:cred`), `timeout_ms` (default 10 000), `enabled`. The credential is write-only in the API: responses carry `credentialSet: true` (`x-hello-secret`), never the value; PUT without a credential keeps it. Only administrators create, rotate or delete servers; operators only attach existing ones.
- [S-6] **Attachment and allowlist.** An agent attaches any number of servers (`voice_agent_mcp`: agent, server, `tools` allowlist, `enabled`). The allowlist is explicit: no tool is available until named (an empty list attaches nothing, and the editor says so). Per tool, `confirm` (default `true` for a tool the server marks non-read-only or leaves unannotated, `false` for `readOnlyHint: true`): talking-agent reads the proposed action aloud and requires a spoken confirmation (its existing behaviour). A tool name is `<server>.<tool>`; names that collide within one agent are refused `409`.
- [S-7] **Discovery.** `POST /api/v1/voice/mcp-servers/{id}/discover` makes hello-control call the server's `tools/list` with the official Go SDK client (`github.com/modelcontextprotocol/go-sdk`, already a dependency) and returns names, descriptions, input schemas and annotations for the editor to tick. Nothing is saved by discovery. The request obeys the egress guard of S-8, a 10 s timeout, a 1 MiB response cap, and output is shown as plain text.
- [S-8] **Egress guard.** MCP URLs are checked at save and dialled with a guarded dialer: literal and resolved addresses must be private (RFC 1918, ULA, loopback only when `HELLO_VOICE_ALLOW_LOOPBACK=true` in the lab), re-checked at dial time against rebinding, unless an administrator sets `HELLO_VOICE_ALLOW_PUBLIC_MCP=true`. Redirects are not followed. The same rule is stated for talking-agent in the change list because it is the one that actually calls the tools at call time.
- [S-9] **Test connection.** The same call as discovery, reported as `ok`, `unauthorized`, `unreachable`, `not_mcp` with latency and tool count; shown on the server's page and stored as `last_check_at`/`last_check_status` (never the response body).

### Destination "voice agent"

- [S-10] **Routing destination.** `voice_agent` is a destination kind next to `extension`, `external` and `sip_uri` in internal calls, inbound routes and (as below) ring groups and feature handling. Its value is the agent name. `internal/routing` validates that it exists and is enabled at compile time (a disabled agent is a validation error on a route that names it, like a missing extension, and the API says which route) and compiles it to a `Decision` with `VoiceAgent{Name, SIPUser}` and `Kind: KindInbound` carrying a derived `SIPURI` `sip:<sip_user>@<runtime address>`, so hello-sip's existing `ringURI` path places the call. Routing traces say `Destination: voice agent "support" (sip:va-3f2a9c01@...)`.
- [S-11] **Inbound DIDs.** An inbound route may choose `voice_agent` as its destination: the DID, trunk, schedule and caller-ID transform fields work as for any inbound route (a schedule plus a second route gives office hours and after-hours agents).
- [S-12] **Ring groups.** A ring group member is an extension _or_ a voice agent (a nullable voice-agent column on ring group members, exactly one of the two set). The group's `failure_kind` gains `voice_agent` (the failure target is the agent name). Rules that keep behaviour boring: an agent as member of a `ring-all`, `longest-idle` or `weighted` group is refused at validation (an agent answers instantly and would always win), allowed in `sequential` and `round-robin`; the agent leg ignores `member_delay` skipping and has no DND or forwarding; the common use is the failure target, "nobody picked up, the assistant takes it".
- [S-13] **Internal dialling and transfer.** Dialling an agent's extension from a phone, a blind or attended transfer to it, and a feature code with argument that names an extension work through the normal internal path, because the extension resolves to the agent. Voicemail is not offered for an agent.
- [S-14] **An agent only answers.** hello-sip accepts no INVITE or REGISTER that originates from the voice agent's address as a caller (it is not a trunk and not an extension); outbound agent calls and click-to-call are out of scope.
- [S-15] **Call setup to the agent.** Hello's hello-sip is the B2BUA and keeps the anchored media path. Towards the agent it:
  - sends `INVITE sip:<sip_user>@<HELLO_VOICE_SIP_ADDRESS>` from its own address, offering PCMU and PCMA only (a caller whose offer has neither, and which Hello cannot transcode, gets `488`; no transcoding is added), with `ptime` 20;
  - adds `X-Hello-Tenant` (`HELLO_VOICE_TENANT`, default the cluster's name), `X-Hello-Agent` (the agent name), `X-Hello-Correlation-Id` (the CDR's `correlation_id`), `X-Hello-Caller` (the presented caller number or `anonymous`), `X-Hello-Called` (the DID or extension dialled), `X-Hello-Ts` (Unix seconds) and `X-Hello-Auth` (S-16); none of these is ever copied from the caller's INVITE, and any such header arriving from the caller is stripped;
  - maps the agent's responses like any callee: `404` (unknown persona) and `403` (rejected) are a failed destination, `486`/`503` (full, down) fall through to the ring group's failure step or the route's reject, `200` answers; a ring timeout of 15 s applies to an agent that does not answer, and the caller hears the answer only after the agent's 200.
- [S-16] **Authenticating the call (shared secret, not IP trust).** `X-Hello-Auth` is `v1:<key id>:<hex HMAC-SHA256>` over `tenant|agent|sip_user|correlation|ts` with the shared secret `HELLO_VOICE_SIP_SECRET` (administrator-created, rotatable: two keys accepted, `key id` selects). talking-agent verifies it, rejects a timestamp more than 30 s off, and rejects a replayed correlation id. `allowed_peers` (and on kw `SIP_TRUSTED_FORWARDERS`) stay as a second layer, not the only one. The secret never appears in logs, the API or the console (shown once on creation, like a device secret). SIP over TLS and SRTP are out of scope.
- [S-17] **Address.** `HELLO_VOICE_SIP_ADDRESS` (`host:port`, UDP by default, unset = voice agents disabled: the registry still edits but routing to an agent is a validation error `voice_not_configured`). kw: `192.168.10.142:5060`.

### Runtime API and live reload (what talking-agent reads)

- [S-18] **Service account and scope.** A new scope `voice-runtime`, grantable only to service accounts (never by OAuth consent, never in the consent screen, never to an MCP client), minimum role `admin` to create. It allows exactly the runtime operations of S-19 to S-21 and nothing else; a `read`/`write` token cannot call them and a `voice-runtime` token cannot call anything else. The runtime operations are `x-hello-mcp: false`.
- [S-19] **Personas for the runtime.** `GET /api/v1/voice-runtime/agents` returns every enabled agent with its persona, limits, and attached MCP servers with the **unsealed** credential and the allowlist, plus the `tenant`, a `revision` and a strong `ETag` (the global `voice_revision`). `If-None-Match` answers `304`. `?wait=25&revision=<n>` long-polls: it answers when the revision moves or after `wait` seconds (`304`), so changes reach talking-agent in under two seconds without a push channel. `Cache-Control: no-store`. The response is marked `x-hello-secret` in the document so MCP, the assistant and the AI proposals (which refuse secret-bearing responses) can never read it; the audit row of each fetch names the service account, not the content.
- [S-20] **Runtime acknowledgement.** `POST /api/v1/voice-runtime/ack` records `{revision, loaded: [{agent, revision}], version}` from talking-agent; the console shows `last_seen_at`, the revision talking-agent runs, and per agent "live", "stale (revision n)" or "not loaded". Rate limit 6 per minute per service account. This is the only write the runtime makes besides S-21.
- [S-21] **Call report.** `POST /api/v1/voice-runtime/calls` accepts `{correlationId, agent, startedAt, endedAt, outcome (completed|caller_hangup|timeout|error|no_speech|transferred), summary (up to 2000), toolCalls: [{name, ok, ms}], tokens: {in, out}, transcript (optional, up to 64 KiB), error}` and is idempotent on `correlationId`. It is accepted for a correlation id Hello has not written a CDR for yet (hello-sip writes the CDR at hangup; the report may arrive first) and attaches by join, not by foreign key. Unknown agent `404`; a body above 128 KiB `413`. The transcript is dropped unless the agent has `record_transcript` (S-23).
- [S-22] **Hello never depends on the runtime.** Routing, CDRs and the registry work when talking-agent has not polled for hours; only the status of S-30 turns red. (talking-agent's side, keeping its last good set when Hello is unreachable, is item 1 of the talking-agent changes.)

### CDR, summary, privacy, retention

- [S-23] **CDR fields.** `cdrs` gains `voice_agent_id` (nullable, `ON DELETE SET NULL`) and `voice_agent_name` (the name at call time, so a deleted agent's calls stay readable), written by hello-sip when the call was routed to an agent, and `voice_agent_outcome` from the report once it arrives (joined, not stored in the CDR row: `voice_agent_calls`, Data). `listCDRs` gains the filter `voiceAgent=<name>`; the CDR detail returns `voiceAgent: {name, outcome, summary, toolCalls, tokens, transcriptAvailable}`. Per agent `record_transcript` (default **off**) and `transcript_retention_days` (default 30, 1-365); summaries follow the CDR retention. The CDR detail marks calls to an agent "automated" for compliance.
- [S-24] **Recording consent.** The greeting is the place to say that the call is handled by an automated assistant; the editor warns when the greeting is empty, and when `record_transcript` is on says so in the preview ("the caller is told only if the greeting says so").

### Console

- [S-25] **Voice agents page** (`web/src/pages/VoiceAgents.tsx`, new): list with name, extension, state (enabled, live/stale/not loaded), attached servers count, calls in 24 h and answer outcome mix; create from a short wizard (name, persona template, language, greeting, number), no JSON anywhere.
- [S-26] **Editor** (the agent editor page): tabs Persona (prompt with a character counter and starter text, greeting, language, voice, style), Tools (attached MCP servers, tool checklist from discovery with `confirm` toggles and "no tool is available until you tick it"), Limits, Numbers (extension, plus a read-only "reachable through" list computed from routes, ring groups and feature codes that name it, with links), History (versions with restore, S-4), Calls (this agent's CDRs with summary). Viewers see it read-only.
- [S-27] **MCP servers page** (the MCP servers page): list with last check status; create/edit (admin only for credentials: operators see the form without the credential field), "Test connection", "Discover tools" results as plain text, "used by" list.
- [S-28] **Routing pickers.** The inbound route editor, the ring group editor (member picker, failure target) and the extension picker offer "Voice agent" as a destination with the agent's name; a disabled agent is greyed with the reason; the route test page (`routingTest`) shows the voice agent step in its trace.
- [S-29] **Test call.** The editor's Test tab shows the agent's extension (or a temporary test extension if it has none, valid 15 minutes, created and removed by the console, audited), the live runtime status ("talking-agent live, running revision n"), and then follows the call: the next CDR to that agent appears there with its outcome and summary within the report latency. Hello places no call for the operator (S-14).
- [S-30] **Runtime page and dashboard card:** talking-agent's `last_seen_at`, revision lag, calls to agents, failures by reason; a red state when the runtime has not been seen for 2 minutes while an enabled agent is routed.

### RBAC, metrics, audit, deployment

- [S-31] **Roles.** `viewer` reads agents, servers (without credentials), runtime status, calls and summaries (transcript: `admin` only). `operator` writes agents, attachments and routes. `admin` writes server credentials, the runtime service account and the SIP secret. Every route has a minimum role in `internal/api/routes.go` and `x-hello-role` in the document; the MCP server exposes read tools for agents and write tools for operators like other resources, but never a credential, a transcript or a runtime operation.
- [S-32] **Metrics and audit.** `hello_voice_calls_total{agent,outcome}` (from the report; `outcome` is `unreported` after 5 minutes without one), `hello_voice_call_setup_seconds` (INVITE to 200), `hello_voice_agent_unreachable_total{reason}` (`timeout`, `403`, `404`, `486`, `503`), `hello_voice_runtime_last_seen_seconds`, `hello_voice_runtime_revision_lag`, `hello_voice_mcp_check_total{status}`, `hello_voice_active_calls{agent}`. Every create, update, attach, detach, restore, credential change and SIP key rotation writes an audit row (fields changed, never the text of a prompt, credential or transcript).
- [S-33] **kw deployment.** hello-control gets `HELLO_VOICE_SIP_ADDRESS=192.168.10.142:5060`, `HELLO_VOICE_TENANT=kw`, and the SIP secret from a SOPS Secret `hello-voice` (optional, like `hello-ai`); hello-sip gets the same address and secret. The runtime service account's token is created once in the console and placed in talking-agent's own Secret by the operator (never committed). talking-agent's deployment (not this repo) lists `https://hello.kw.watteel.lab` and that token as tenant `kw`. Sources of RTP: hello-sip anchors media, so the agent sees hello-sip's pod/node address; the `talking-agent-sip` Service source ranges and `SIP_TRUSTED_FORWARDERS` in Sync must include it (verified at deploy, `TestKwVoiceAgents`). Documented in `docs/voice-agents.md`.
- [S-34] **Docs.** `docs/voice-agents.md`: model, creating an agent in five steps, MCP servers and tool confirmation, routing recipes (DID, after-hours, ring group fallback, extension), the call headers and signature, the runtime API and its service account, CDR fields, privacy and retention, kw deployment, troubleshooting by response code; generated tables of the runtime operations and the limits; `TestDocsVoiceAgents` fails if they drift.

## Out of scope

- Hosting STT, TTS, the LLM loop or MCP execution inside Hello. talking-agent keeps all of it.
- Outbound calls placed by an agent, click-to-call, WebRTC test calls (S-29 explains what is offered).
- SIP TLS/SRTP between Hello and talking-agent; a SIP trunk or REGISTER relationship (talking-agent's gateway REGISTER mode is not used: Hello sends INVITEs to it).
- Transcoding to or from anything but G.711 between caller and agent.
- Per-tenant isolation inside Hello (Hello is single-tenant; "tenant" names the Hello instance to talking-agent).
- Agents authoring routes or changing Hello configuration. An agent holds no Hello credential; if an operator wants it to operate Hello it attaches Hello's own MCP server like any other, with a token they choose (the existing phase 1 scopes bound it).
- Streaming live transcripts into the console, sentiment, analytics beyond S-30.
- OAuth flows for third-party MCP servers (bearer and header credentials only; see Open questions).
- Billing and quotas per agent beyond the limits of S-2.

## Constraints

- Hello's SIP hot path changes only where S-15 says: a new header set on the existing `ringURI` leg and a CDR field. No new goroutine on the call path; the secret and agent lookup come from the compiled routing table (loaded off the path like every route).
- No new direct Go dependency (stdlib `crypto/hmac`, `internal/secret`, the existing MCP SDK and `internal/apispec`).
- The runtime response is the only place an unsealed MCP credential exists; it is never logged, cached by Hello, or returned to any other principal.
- One service account per Hello instance by default; revoking it stops persona reads at once and talking-agent keeps its last good set (S-22).
- The routing engine stays pure: it knows agent names and sip users from its input, not from the database (like `Extensions`).
- Everything runs on kw or in CI; nothing starts on a developer Mac.

## Interfaces

New operations (all `/api/v1`; scope, minimum role, `x-hello-mcp`):

| Operation                                                              | Method and path                                                    | Scope / role                           | MCP                           |
| ---------------------------------------------------------------------- | ------------------------------------------------------------------ | -------------------------------------- | ----------------------------- |
| `listVoiceAgents`, `getVoiceAgent`                                     | `GET /voice/agents[/{id}]`                                         | read / viewer                          | read tool                     |
| `createVoiceAgent`, `updateVoiceAgent`, `deleteVoiceAgent`             | `POST`, `PUT`, `DELETE /voice/agents[/{id}]`                       | write / operator                       | write tool                    |
| `listVoiceAgentVersions`, `restoreVoiceAgentVersion`                   | `GET`, `POST /voice/agents/{id}/versions[/{v}/restore]`            | read viewer, write operator            | yes                           |
| `putVoiceAgentTools`                                                   | `PUT /voice/agents/{id}/tools` (attachments and allowlists)        | write / operator                       | write tool                    |
| `listVoiceAgentCalls`                                                  | `GET /voice/agents/{id}/calls` (CDR join)                          | read / viewer                          | read tool                     |
| `getVoiceStatus`                                                       | `GET /voice/status` (runtime last seen, revision, per-agent state) | read / viewer                          | read tool                     |
| `listVoiceMCPServers`, `getVoiceMCPServer`                             | `GET /voice/mcp-servers[/{id}]`                                    | read / viewer                          | read tool (no credential)     |
| `createVoiceMCPServer`, `updateVoiceMCPServer`, `deleteVoiceMCPServer` | `POST`, `PUT`, `DELETE`                                            | admin / admin                          | not exposed                   |
| `discoverVoiceMCPServer`, `testVoiceMCPServer`                         | `POST /voice/mcp-servers/{id}/discover`, `/test`                   | write / operator                       | not exposed (egress)          |
| `rotateVoiceSIPSecret`, `createVoiceRuntimeAccount`                    | `POST /voice/secret/rotate`, `/voice/runtime-account`              | admin / admin; secret `x-hello-secret` | not exposed                   |
| `getVoiceRuntimeAgents`                                                | `GET /voice-runtime/agents`                                        | voice-runtime / service account        | not exposed, `x-hello-secret` |
| `ackVoiceRuntime`, `reportVoiceCall`                                   | `POST /voice-runtime/ack`, `/calls`                                | voice-runtime                          | not exposed                   |

Existing operations extended: inbound routes and ring groups accept the `voice_agent` kind; `listCDRs` filter `voiceAgent`; CDR detail `voiceAgent`; `routingTest` trace; the dashboard summary gains `voiceAgents`.

Configuration (hello-control and hello-sip): `HELLO_VOICE_SIP_ADDRESS`, `HELLO_VOICE_SIP_SECRET` (and `_NEXT` during rotation), `HELLO_VOICE_TENANT`, `HELLO_VOICE_MAX_AGENTS`, `HELLO_VOICE_ALLOW_PUBLIC_MCP`, `HELLO_VOICE_ALLOW_LOOPBACK`.

SIP towards talking-agent: the headers of S-15 and the signature of S-16. Errors use the standard `Error` schema with new codes `voice_not_configured`, `voice_agent_limit`, `voice_agent_disabled`, `voice_tool_conflict`, `mcp_endpoint_not_private`.

## Data

`migrations/00010_voice_agents.sql` with its down migration:

- `voice_agents` (id, name unique, description, enabled, sip_user unique, extension nullable unique, prompt, greeting, language, voice, voice_reference, style, temperature, max_call_seconds, max_concurrent, max_tool_calls, idle_timeout_seconds, record_transcript, transcript_retention_days, revision, created_at, updated_at; `CHECK` lists on every range)
- `voice_agent_versions` (agent_id cascade, revision, persona JSON, actor, created_at; last 10 kept)
- `voice_mcp_servers` (id, name unique, url, auth CHECK in (`none`,`bearer`,`header`), header_name, credential `bytea` sealed, timeout_ms, enabled, last_check_at, last_check_status)
- `voice_agent_mcp` (agent_id cascade, server_id restrict, enabled, tools JSON `[{name, confirm}]`, primary key agent+server)
- a nullable voice-agent column on ring group members nullable references `voice_agents` restrict; `extension_id` becomes nullable; `CHECK (num_nonnulls(extension_id, voice_agent_id) = 1)`; the unique position constraint is unchanged. `ring_groups.failure_kind` CHECK gains `voice_agent`. `inbound_routes.destination_kind` CHECK gains `voice_agent`.
- `voice_runtime` (singleton: revision, last_seen_at, loaded JSON, version, service account id)
- `voice_agent_calls` (correlation_id primary key, agent_name, outcome, summary, tool_calls JSON, tokens_in, tokens_out, transcript text nullable, reported_at; pruned by retention hourly with an advisory lock, like `ai` prune)
- `cdrs.voice_agent_id`, `cdrs.voice_agent_name`, index on `voice_agent_name, start_time`.
- `voice_revision` is a single counter bumped in the same transaction as any persona, attachment, server (credential included), enable or limit change, so the ETag changes exactly when the runtime's view does. Deleting an agent referenced by a route, ring group member or feature code answers `409` naming the references.

## Edge cases

- Agent renamed: routes hold the id internally and show the name; CDR `voice_agent_name` keeps the old name for old calls.
- Agent disabled while a call is in progress: the call continues; new calls get the route's failure path; routes naming it show a validation error banner (the API refuses to disable an agent that a route names unless `force=true`, then routes fail with reason `voice agent disabled` in the trace).
- A persona edited mid-call: the call keeps its persona (talking-agent snapshots at call start, already its behaviour).
- Two agents with the same extension, or an agent extension equal to an extension: `409`.
- The runtime reloads while the console is saving twice quickly: each save is one revision; the runtime applies the latest.
- Two Hello instances (lab and kw) use one talking-agent: tenants and per-tenant secrets keep `sip_user` namespaces separate.
- Caller hangs up while the agent is setting up: normal CANCEL, CDR final status `487`, outcome `unreported` if no report.
- Report arrives twice or for an unknown correlation id: idempotent; the unknown one is stored and pruned if no CDR ever matches within retention.
- Anonymous caller: `X-Hello-Caller: anonymous`; the agent must never use caller ID for identity (talking-agent already so).
- An MCP server rotates its credential: the operator updates it once in Hello; the revision moves; talking-agent reloads without restart.
- A tool name the server removed after attachment: the allowlist keeps it; the runtime sends it; the console shows "not offered by the server" after the next discovery; the call simply lacks that tool.
- Prompt containing a secret-looking string: stored as given (it is configuration), excluded from audit and logs; the editor warns against pasting credentials.

## Failure modes

- **talking-agent down or unreachable:** INVITE times out in 15 s (`503` or no answer): the call follows the ring group's next step or the route's failure, `hello_voice_agent_unreachable_total{reason="timeout"}` moves, the runtime status turns red after 2 minutes of silence from its poll, and the CDR carries the agent with outcome `unreported`.
- **Hello API down:** talking-agent runs its last good set (S-22); persona edits wait; Hello's routing needs the database anyway.
- **Bad or rotated SIP secret:** talking-agent answers `403`; calls fail as failed destinations, with a distinct metric reason `403` and a troubleshooting line; rotating with two keys accepted avoids it.
- **An MCP server is down at call time:** talking-agent tells the caller the tool is unavailable and continues; its report lists the failed tool call.
- **Capacity:** `486` from talking-agent: next step in the group or `486` to the caller; counted.
- **Valkey or PostgreSQL unavailable:** existing behaviour; registry reads answer `503`; the compiled routing table in memory keeps routing to agents already known.
- **Runtime token leaked:** revoke the service account; the console's "create runtime account" rotates it; the old token fails `401` at once; MCP credentials that were in the response should be rotated, and the docs say so.
- **Report lost:** summary missing; CDR still complete; outcome `unreported`.

## Acceptance criteria

- [ ] [S-1] [S-2] [S-3] [S-4] `TestVoiceAgentCRUD` in `internal/api` passes. It fails if a name outside the pattern, a prompt over 8000 characters, an out-of-range limit, a duplicate extension (agent or extension), or an eleventh version is accepted or kept, if `sip_user` can be edited, if a restore rewrites history instead of adding a revision, or if an audit row holds prompt text.
- [ ] [S-5] [S-6] `TestVoiceMCPServers` in `internal/api` and `internal/store` passes. It fails if a credential is returned by any operation other than the runtime one, is stored unsealed, is changed by a PUT without one, if an operator can set one, if an empty allowlist attaches a tool, if a read-only-hinted tool defaults to `confirm` or an unannotated one does not, or if two attached tools collide.
- [ ] [S-7] [S-8] [S-9] `TestVoiceMCPDiscovery` in `internal/voice` passes against a fake MCP server (`test/fakemcp`). It fails if a public or rebinding address is dialled without the opt-in, a redirect is followed, a response over 1 MiB is read, discovery saves anything, or the check result stores a response body.
- [ ] [S-10] [S-11] `TestVoiceAgentRouting` in `internal/routing` passes. It fails if an inbound route or an extension that names a missing, disabled or unconfigured agent compiles, if the decision lacks the agent's `sip_user` URI, if schedules and trunk filters stop applying, or if the trace omits the agent step.
- [ ] [S-12] [S-13] `TestVoiceAgentRingGroup` in `internal/api` and `internal/routing` passes. It fails if an agent member of `ring-all`, `longest-idle` or `weighted` is accepted, if a member row has both or neither of extension and agent, if the failure target `voice_agent` does not take the call after the group times out, or if a transfer to an agent extension does not reach it.
- [ ] [S-14] [S-15] `TestVoiceAgentInvite` in `internal/sip` passes against `test/sipua` as a fake voice agent. It fails if hello-sip accepts an INVITE or REGISTER that originates from the voice agent's address as a caller (S-14), the INVITE lacks any header of S-15, copies one from the caller, offers a codec other than PCMU/PCMA, or if `404`, `403`, `486`, `503` and silence are not each mapped to the failure path with the right reason.
- [ ] [S-16] `TestVoiceCallAuth` in `internal/sip` and `internal/voice` passes. It fails if the signature does not verify with a vector shared with talking-agent (`testdata/voice_auth_vectors.json`), a field change does not break it, the secret appears in a log or the API, or two keys are not both accepted during rotation.
- [ ] [S-17] `TestVoiceDisabledWithoutAddress` in `internal/config` and `internal/routing` passes. It fails if a route to an agent compiles with no `HELLO_VOICE_SIP_ADDRESS`, or the registry stops editing.
- [ ] [S-18] [S-19] `TestVoiceRuntimeAPI` in `internal/api` passes. It fails if a token without `voice-runtime` reads it, a `voice-runtime` token reads anything else, OAuth consent can grant the scope, the response lacks an unsealed credential or an ETag, `If-None-Match` does not answer `304`, a long poll does not return within `wait` or on a revision change within 2 s, a disabled agent appears, or the operation is reachable through MCP, the assistant or a proposal.
- [ ] [S-20] [S-22] [S-30] `TestVoiceRuntimeStatus` in `internal/api` passes. It fails if routing, CDRs or registry edits stop while the runtime has not polled (S-22), an ack does not set `last_seen_at`, the revision lag or per-agent state, a seventh ack in a minute is accepted, or status stays green after 2 minutes of silence with an agent routed.
- [ ] [S-21] [S-23] `TestVoiceCallReport` in `internal/api` and `internal/cdr` passes. It fails if a report is not idempotent, is lost when it arrives before its CDR, stores a transcript without `record_transcript`, exceeds its size limits unrejected, or if `listCDRs` lacks the `voiceAgent` filter or the CDR detail the `voiceAgent` object.
- [ ] [S-23] `TestVoiceCDRFields` in `test/integration` passes. It fails if a call routed to an agent writes a CDR without `voice_agent_id` and name, a call that is not routed to one writes them, or deleting the agent erases the name from past CDRs.
- [ ] [S-23] `TestVoicePrune` in `internal/voice` passes. It fails if transcripts outlive their retention, summaries outlive the CDR retention, or two replicas prune at once.
- [ ] [S-25] [S-26] [S-27] [S-24] [S-28] [S-29] [S-30] `procoder test` and `procoder lint` over `web/` pass. `VoiceAgents.test.tsx`, `VoiceAgentEdit.test.tsx`, `VoiceMCPServers.test.tsx` and `VoiceRouting.test.tsx` fail if a viewer sees an edit control, an operator sees a credential field, a credential value is rendered, discovery output is rendered as HTML, an empty allowlist hides its warning, the routing pickers omit the voice agent, a disabled agent is selectable, or the Test tab does not follow the next CDR.
- [ ] [S-31] phase 1's `TestRoutesMatchOpenAPI`, `TestOpenAPIConformance`, `TestOpenAPIForTools`, `TestRoleEnforcement` and `TestToolsFromOpenAPI` pass with the new operations, and `TestVoiceOperationsMCP` in `internal/mcp` fails if a credential, transcript, runtime or egress operation is an MCP tool or an agent write is a read tool.
- [ ] [S-32] `TestVoiceMetrics` in `internal/voice` and `TestNoSecretsInLogs` cover voice. They fail if a call, setup, unreachable reason, runtime check or MCP check does not move its metric, or a prompt, credential, signature or transcript appears in a log line.
- [ ] [S-33] `TestKwVoiceAgents` in `test/deploy` passes. It fails if the kw manifests do not set `HELLO_VOICE_SIP_ADDRESS` to the `talking-agent-sip` address, reference Secret `hello-voice` optionally, or commit a secret or token.
- [ ] [S-34] `docs/voice-agents.md` exists and covers every item of S-34; `TestDocsVoiceAgents` in `test/deploy` fails if its operation table or limits differ from the code, or the README does not link it.
- [ ] [S-10] [S-15] [S-16] [S-19] [S-21] `TestVoiceAgentsEndToEnd` in `test/integration` passes in the lab with a fake talking-agent (`test/fakeagent`: a SIP UA that verifies `X-Hello-Auth`, answers, plays a tone, polls the runtime API with a service account, reloads on a revision change within 2 s, and posts a call report). It fails if creating an agent and an inbound route through the API does not make a call from a fake phone reach it, a persona edit does not reach the fake within 2 s, an unsigned or mistimed INVITE is accepted, an agent as ring group failure target does not take the call after the timeout, the CDR lacks the agent and report, or a disabled agent still answers a new call.
- [ ] [S-33] `TestKwSmokeVoice` (`HELLO_KW_SMOKE=1`, against the real talking-agent on kw after its changes land) passes. It fails if a call to a test agent through a kw DID does not answer with the greeting, call one attached MCP tool, appear in the CDRs with its summary, and end cleanly.

## Required changes in talking-agent (not built in Hello)

Tracked in `github.com/piwi3910/talking-agent`; Hello's `TestKwSmokeVoice` needs the first six. Each is an issue there, written from this list.

1. **Personas from Hello with live reload.** A `HelloSource` replaces the local `PHONE_SETTINGS_FILE` as the source of personas and numbers per tenant: fetch `GET /api/v1/voice-runtime/agents`, long-poll with `ETag` and `?wait=25`, atomically swap the set, snapshot it to disk, run from the snapshot when Hello is unreachable (S-22), post `ack` with the loaded revision. New calls use the new set, running calls keep theirs. The local settings UI becomes read-only for Hello-managed tenants (local personas stay for standalone use).
2. **MCP client implementing `tools.Executor` per agent.** An `MCPExecutor` (official Go SDK client, Streamable HTTP) that builds an agent's `tools.Definition` list from the allowlist (names, schemas, annotations from `tools/list`), caches sessions per server credential, enforces the S-8 egress guard, timeouts and result size caps, redacts credentials from logs and telemetry, honours `confirm` through the existing spoken-confirmation path, treats tool results as untrusted text, and enforces `max_tool_calls`. The memory/skills tools stay; MCP tools are additional and per agent.
3. **Multi-tenant auth instead of IP trust.** Tenants configured as `{id, hello_url, service token, sip keys}`; verify `X-Hello-Auth` (S-16: HMAC, 30 s skew, replay cache by correlation id), select persona by `(tenant, sip_user)`, never by Request-URI alone; keep `allowed_peers` as a second layer only; state per-tenant isolation of memory, history, sessions, limits and metrics.
4. **Call report.** Post the report of S-21 at hangup with outcome, summary (an LLM-generated short summary through go-ai-sdk, off for agents that disable it), tool call list and token counts; transcript only when the persona says so.
5. **Persona fields.** Honour `greeting`, `language`, `voice`, `voice_reference`, `style`, limits (`max_call_seconds`, `max_concurrent`, `idle_timeout_seconds`) and answer `486` at the per-agent as well as global cap.
6. **Speech-model concurrency and queueing.** STT and TTS models are single-GPU resources: a bounded queue with per-tenant fairness, admission control (`486` before answering rather than a stalled call), per-call latency metrics, and a load test showing N concurrent calls hold a stated first-word latency. Documented in its README with the numbers measured on kw.
7. **Better phone-path VAD.** Replace the 128 ms energy detector with a proper VAD (Silero or WebRTC VAD adapted to 8 kHz), configurable endpointing (the 700 ms trailing silence), barge-in that works through G.711 line noise, noise gate tests with recorded handset audio, and comfort-noise/DTX handling.
8. **Proper resampler.** A polyphase band-limited resampler for 8 kHz to 16/24 kHz and back (not linear interpolation), tested against a spectral reference, so STT accuracy on phone audio is measured and TTS output does not alias.
9. **Pluggable TTS.** An interface behind which Breeze TTS 2 is the lab implementation and a commercial engine can be added (voice ids, streaming, 24 kHz output), selected per persona or per deployment.
10. **All LLM calls through go-ai-sdk** (decision of 2026-10-07), including the summary of item 4; a provider endpoint per deployment, private-by-default as in Hello's phase 2.
11. **Licence.** `LICENSE.md` reads `ok`, which is not a licence. A real licence must be chosen before anyone else deploys or contributes, and the research-only Breeze TTS weights and any other model licences must be listed in its README with what they allow (Open question 6).
12. **SIP hardening.** SIP TLS and SRTP (separate follow-up, Open question 5), request rate limit per peer, no unauthenticated `OPTIONS` detail, `Min-Expires` and call limits on registration mode left as is.

## Open questions

1. **Who is the audience of "tenant"?** Is talking-agent shared by several Hello instances (lab, kw, perhaps a customer), or only by Hello on kw plus its own web UI? This spec supports many Hello tenants with a per-tenant secret and service account; if there is only one, S-16's key ids and the tenant header can collapse to one secret and one account. (Assumed: many, because the decision calls it multi-tenant.)
2. **Transcripts.** Default is summaries only, transcripts off per agent and readable by admins only, 30 days. Is that right for your use, and do you need transcripts at all (recorded-calls law differs by country, and an LLM summary is itself derived personal data)?
3. **MCP credentials beyond bearer/header.** Real MCP servers use OAuth 2.1. Is a static token or header enough for the servers you will attach (for example Hello's own MCP server, home-automation, calendar), or must a voice agent act through OAuth on behalf of a user (a much bigger design: token refresh, per-user consent, caller identity)?
4. **Caller identity and tool authority.** Today a persona runs with the tools' credentials regardless of who calls (caller ID is never identity). Do you want any caller verification (spoken PIN, known-caller list per agent) before mutating tools, or is "confirm aloud" the whole control?
5. **TLS/SRTP between Hello and talking-agent.** Both sit on the same LAN on kw. Is plaintext SIP/RTP acceptable on kw for phase 3 with the HMAC signature (S-16), or is SIPS/SRTP a requirement before first use?
6. **Licence of talking-agent and Breeze.** `LICENSE.md` contains only `ok`; Breeze TTS 2 weights are research-only. Which licence should talking-agent have, and is any commercial or customer use of the lab voice intended before a commercial TTS is chosen?
7. **Agents in ring groups.** The spec forbids an agent in `ring-all`/`longest-idle`/`weighted` groups and recommends it as the failure target. Do you also want it as a normal `sequential` member (for example "ring the desk phone, then the assistant"), or only as a failure target?
8. **Test call.** The spec offers a test extension and CDR following, not a browser or console-originated call. Do you want Hello to originate a call to a chosen phone and connect it to the agent (needs click-to-call, which Hello does not have), or a WebRTC softphone in the console (a new media stack)?
9. **Where limits are enforced.** Concurrency is enforced by talking-agent (`486`). Should Hello also cap simultaneous agent calls per agent or globally (it knows the calls and could refuse before sending an INVITE), accepting that two sources then hold the number?
