# The in-product AI agent

Hello can reason about itself. The agent is suggest-only: an assistant in the console answers questions from live data and drafts changes, deterministic detectors find PBX problems and a model explains them, and a human reviews every proposed change as a diff before anything is applied. For connecting an external agent such as Claude Code to Hello, see [docs/ai-access.md](ai-access.md).

## Enable it

The agent is on only when both `HELLO_AI_BASE_URL` and `HELLO_AI_MODEL` are set on hello-control; the API key may be empty. Otherwise `GET /api/v1/ai/status` reports `enabled: false` with `not_configured` or `incomplete_configuration`, no model connection opens, detectors do not run, every other AI operation answers `503` `ai_disabled`, and the console hides the assistant.

| Variable                             | Default       | Meaning                                                             |
| ------------------------------------ | ------------- | ------------------------------------------------------------------- |
| `HELLO_AI_PROVIDER`                  | `openai`      | `openai` (any OpenAI-compatible server) or `anthropic`              |
| `HELLO_AI_BASE_URL`                  | unset         | the endpoint, for example `http://host:8000/v1`                     |
| `HELLO_AI_MODEL`                     | unset         | the model name                                                      |
| `HELLO_AI_API_KEY`                   | empty         | the key; never stored, logged or shown                              |
| `HELLO_AI_ALLOW_PUBLIC_ENDPOINT`     | `false`       | allow an endpoint that resolves to a public address                 |
| `HELLO_AI_STRUCTURED_OUTPUT`         | `json_schema` | `json_schema`, or `prompt` for servers without `response_format`    |
| `HELLO_AI_THINKING`                  | `off`         | `off` asks the model not to reason before answering; `on` allows it |
| `HELLO_AI_VALIDATION_ATTEMPTS`       | `3`           | asks again this often when an answer is invalid                     |
| `HELLO_AI_MAX_STEPS`                 | `8`           | model steps per assistant message (and at most 16 tool calls)       |
| `HELLO_AI_MAX_CONCURRENCY`           | `2`           | model calls in flight per replica                                   |
| `HELLO_AI_REQUESTS_PER_MINUTE`       | `30`          | model calls started per minute per replica                          |
| `HELLO_AI_TIMEOUT`                   | `180s`        | one model call                                                      |
| `HELLO_AI_DAILY_TOKEN_BUDGET`        | `2000000`     | tokens per UTC day across replicas                                  |
| `HELLO_AI_BACKGROUND_BUDGET_PERCENT` | `80`          | background work stops at this share of the budget                   |
| `HELLO_AI_AGENT_START_DELAY`         | `2m`          | first run of an agent after start                                   |
| `HELLO_AI_AIOPS_INTERVAL`            | `60s`         | the detector agent; `0` disables it                                 |
| `HELLO_AI_EXPLAIN_MIN_INTERVAL`      | `10m`         | the model explains the open findings at most this often             |

`HELLO_AI_THINKING=off` applies to every model call (assistant answers, streaming, detector explanations) with provider `openai`: Hello adds `chat_template_kwargs: {"enable_thinking": false}` to the request body, which vLLM passes to the chat template of reasoning models such as Qwen3, and which other templates ignore. Reasoning tokens are never shown to users, and a model that reasons past `max_tokens` ends on `length` and costs a retry at double size, so off keeps answers to seconds. Servers that reject unknown request fields need `on`. The setting is not sent to provider `anthropic`. Hello does not send `reasoning_effort`.

On kw the connection settings come from Secret `hello-ai` (`provider`, `base-url`, `model`, `api-key`, SOPS-encrypted in `deploy/kuvryn-sync/kw/secret-hello-ai.sops.yaml`), referenced optionally by hello-control, so without the Secret the agent stays off. kw uses the local fastllm proxy (`qwen3-6-35b-a3b`), private only.

## Privacy and public endpoints

The endpoint host must resolve only to loopback, RFC 1918, RFC 6598, IPv6 unique-local or link-local addresses; otherwise the agent stays off with `endpoint_not_private`. The check runs at start and again in the dialer before every connection, so a host that later resolves to a public address fails the call. `HELLO_AI_ALLOW_PUBLIC_ENDPOINT=true` is the administrator's explicit opt-in: PBX data (phone numbers, names, User-Agents, call records) is then sent to that endpoint as-is, and the console shows it unmasked. The choice of endpoint is the only control. Text copied from SIP traffic or the database reaches the model inside `<data>` tags, with an instruction never to follow it.

## What the assistant can and cannot do

The assistant answers questions by calling read tools. Each call is replayed through Hello's own API as the asking user, so a user sees only what their role allows, and the answer lists the calls it relies on. It cannot change anything: it has no write tool. When asked for a change it drafts a proposal that a human applies. It does not see secrets (SIP secrets, provisioning URLs, API keys). Its answers are plain text.

The tools are fixed, all `GET` operations with scope `read`:

| Tool                    | Operation                               | What it reads                                                         |
| ----------------------- | --------------------------------------- | --------------------------------------------------------------------- |
| `listExtensions`        | `GET /api/v1/extensions`                | Every extension, ordered by number                                    |
| `getExtension`          | `GET /api/v1/extensions/{id}`           | One extension                                                         |
| `listDevices`           | `GET /api/v1/devices`                   | Every device, ordered by SIP username                                 |
| `getDevice`             | `GET /api/v1/devices/{id}`              | One device (never its secret)                                         |
| `listRingGroups`        | `GET /api/v1/ring-groups`               | List ring/hunt groups                                                 |
| `getRingGroup`          | `GET /api/v1/ring-groups/{id}`          | One ring/hunt group                                                   |
| `listFeatureCodes`      | `GET /api/v1/feature-codes`             | List DTMF feature codes                                               |
| `listTrunks`            | `GET /api/v1/trunks`                    | Every trunk (never its password)                                      |
| `listTrunkStatus`       | `GET /api/v1/trunks/status`             | Live registration, destination health and active calls of every trunk |
| `getTrunk`              | `GET /api/v1/trunks/{id}`               | One trunk (never its password)                                        |
| `listOutboundRoutes`    | `GET /api/v1/routes/outbound`           | Every outbound route in position order                                |
| `getOutboundRoute`      | `GET /api/v1/routes/outbound/{id}`      | One outbound route                                                    |
| `listInboundRoutes`     | `GET /api/v1/routes/inbound`            | Every inbound route in position order                                 |
| `getInboundRoute`       | `GET /api/v1/routes/inbound/{id}`       | One inbound route                                                     |
| `listRegistrations`     | `GET /api/v1/registrations`             | Every registered contact binding in the cluster (from Valkey)         |
| `listCalls`             | `GET /api/v1/calls`                     | Every active call in the cluster (from Valkey)                        |
| `listCDRs`              | `GET /api/v1/cdrs`                      | Call detail records, newest first                                     |
| `countCDRs`             | `GET /api/v1/cdrs/counts`               | How many call records exist, and how many failed                      |
| `cdrConcurrency`        | `GET /api/v1/cdrs/concurrency`          | Recorded calls in progress over a time window, by direction           |
| `getCDR`                | `GET /api/v1/cdrs/{id}`                 | One CDR with its routing trace and failure explanation                |
| `getCluster`            | `GET /api/v1/cluster`                   | Cluster members, dependency health and configuration revision         |
| `listClusterNodes`      | `GET /api/v1/cluster/nodes`             | Every member, live and OFFLINE                                        |
| `getDeviceDiagnostics`  | `GET /api/v1/diagnostics/devices/{id}`  | Why a device is or is not registered                                  |
| `listAuthFailures`      | `GET /api/v1/diagnostics/auth-failures` | Source IPs with failed authentications                                |
| `listPhones`            | `GET /api/v1/phones`                    | Every provisioned phone, ordered by MAC                               |
| `getPhone`              | `GET /api/v1/phones/{id}`               | One phone                                                             |
| `listPhoneFetches`      | `GET /api/v1/phones/{id}/fetches`       | The phone's provisioning fetches, newest first                        |
| `listRecordings`        | `GET /api/v1/recordings`                | List call recordings                                                  |
| `listVoicemailBoxes`    | `GET /api/v1/voicemail/boxes`           | List voicemail boxes                                                  |
| `listVoicemailMessages` | `GET /api/v1/voicemail/messages`        | List a box's voicemail messages                                       |
| `listAIFindings`        | `GET /api/v1/ai/findings`               | AIOps findings                                                        |
| `getAIFinding`          | `GET /api/v1/ai/findings/{id}`          | One finding                                                           |
| `listAIProposals`       | `GET /api/v1/ai/proposals`              | AI proposals                                                          |
| `getAIProposal`         | `GET /api/v1/ai/proposals/{id}`         | One proposal with its diff                                            |

## Streaming answers

A real answer can take a minute or more: a reasoning model, several tool calls. The console therefore shows progress as it happens: an animated typing indicator from the moment you send (it names the step, for example "Looking up registrations…" while a tool runs, or "Thinking…" while the model reasons), then the answer text growing token by token, then the stored message with its data sources and proposal. A Stop button cancels the answer; leaving the page does the same.

The console uses the same call as every other client, `POST /api/v1/ai/sessions/{id}/messages`, with `Accept: text/event-stream`. Without that header the call is unchanged: `202` with a task id, which API clients poll (`getAITask`). With it, a refusal before the work starts is the usual JSON error (`409 task_running`, `429 ai_busy`, `400`, `404`, `503 ai_disabled`); otherwise the answer is `200` and a stream of Server-Sent Events. Each event has a name and one JSON object as its data; a comment line (`: keepalive`) every 15 seconds keeps idle proxies from closing the stream during a long reasoning phase.

| Event      | Data                                                        | Meaning                                                                                                                                                                                                                               |
| ---------- | ----------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `started`  | `{"taskId", "messageId"}`                                   | The question is stored and the task runs (the ids the `202` carries). Always first.                                                                                                                                                   |
| `thinking` | `{}`                                                        | The model is reasoning; there is no answer text yet. Reasoning itself is never sent.                                                                                                                                                  |
| `tool`     | `{"operationId", "label"}`                                  | The assistant is reading data: the tool's operation and a short label for people. Never the arguments.                                                                                                                                |
| `delta`    | `{"text"}`                                                  | The next piece of the answer text. Concatenate them.                                                                                                                                                                                  |
| `reset`    | `{}`                                                        | The text sent so far is void (the model went on to call tools, or its answer was rejected and it was asked again); clear it. More follows.                                                                                            |
| `done`     | `{"taskId", "messageId", "message", "proposalId", "usage"}` | The answer is stored. `message` is the stored assistant message exactly as `getAISession` lists it (content, `toolCalls`, `citations`, `proposalId`); `usage` is `{"inputTokens", "outputTokens", "reasoningTokens", "calls"}`. Last. |
| `error`    | `{"code", "message"}`                                       | The task failed with the error code of the table below, for example `provider_error`, `timeout`, `invalid_output`, `ai_budget_exhausted`. Last; nothing was stored.                                                                   |

Exactly one of `done` and `error` ends a stream, and it is sent after the task is finished, so the next message may be posted at once. The answer is stored exactly as without streaming, by the task, before `done` goes out; a stream that drops once the answer exists loses nothing and does not repeat it. A stream that closes earlier (Stop, navigation, a dropped connection) cancels the model call: the task ends `canceled`, no answer is stored and the question stays in the session.

Only the human answer is streamed. The model still answers with one structured object (`answer`, `citations`, optional `proposal`) in both `HELLO_AI_STRUCTURED_OUTPUT` modes, validated as before; Hello reads the `answer` property out of the partial JSON as it arrives (decoding escapes, skipping any inline `<think>` block and code fence) and passes only that text on. Citations and the proposal arrive complete in `done`. Streamed text is shown before validation: if the answer is rejected, a `reset` clears it before the corrected one streams.

Nothing between the model and the browser may buffer the stream. hello-control sends `X-Accel-Buffering: no` and `Cache-Control: no-cache, no-transform`; the console's nginx has a location for this route with `proxy_buffering off`, compression off and a 600 s read timeout (it also sets `X-Accel-Buffering` again for an ingress controller in front); the kw ingress allows 600 s reads. A different proxy in front of the console needs the same: no response buffering for `POST /api/v1/ai/sessions/*/messages`.

## Proposals and the allowlist

A proposal has a title, a rationale and up to 8 ordered actions, each with the target as it is now (`before`) and as the action would leave it (`after`). Nothing is sent until an operator applies it. Applying replays the actions in order through the API with the applier's own credentials, so roles, validation and audit are those of a direct call; each action is its own configuration change with an audit row whose `via` is `ai-proposal:<id>`. If the targets changed since the proposal was made it becomes `stale` and nothing is sent; the first failing action stops it as `failed`, with the earlier ones listed as applied. Deletes are marked, and the console asks for confirmation naming each operation. A dismissed proposal cannot be applied.

Only these operations may appear in a proposal; anything else, every admin-scoped operation and every operation whose response carries a secret is refused:

| Operation             | Request                               | Role     | Kind                                |
| --------------------- | ------------------------------------- | -------- | ----------------------------------- |
| `updateExtension`     | `PATCH /api/v1/extensions/{id}`       | operator | change                              |
| `updateDevice`        | `PATCH /api/v1/devices/{id}`          | operator | change                              |
| `createRingGroup`     | `POST /api/v1/ring-groups`            | operator | create                              |
| `updateRingGroup`     | `PATCH /api/v1/ring-groups/{id}`      | operator | change                              |
| `deleteRingGroup`     | `DELETE /api/v1/ring-groups/{id}`     | operator | delete (marked, needs confirmation) |
| `putFeatureCodes`     | `PUT /api/v1/feature-codes`           | operator | change                              |
| `createOutboundRoute` | `POST /api/v1/routes/outbound`        | operator | create                              |
| `updateOutboundRoute` | `PATCH /api/v1/routes/outbound/{id}`  | operator | change                              |
| `deleteOutboundRoute` | `DELETE /api/v1/routes/outbound/{id}` | operator | delete (marked, needs confirmation) |
| `createInboundRoute`  | `POST /api/v1/routes/inbound`         | operator | create                              |
| `updateInboundRoute`  | `PATCH /api/v1/routes/inbound/{id}`   | operator | change                              |
| `deleteInboundRoute`  | `DELETE /api/v1/routes/inbound/{id}`  | operator | delete (marked, needs confirmation) |
| `updateTrunk`         | `PATCH /api/v1/trunks/{id}`           | operator | change                              |
| `updatePhone`         | `PATCH /api/v1/phones/{id}`           | operator | change                              |

## Detectors and findings

Detectors are code, not a model: they read PostgreSQL and Valkey directly and produce candidates with evidence. The `aiops` agent runs them, stores findings, and asks the model to explain, correlate and rank the open set when it changed. Without a model answer (off, over budget, failing) a finding shows the detector's own title and evidence with `explained: false`.

| Detector          | Threshold                                           | Value    |
| ----------------- | --------------------------------------------------- | -------- |
| `reg_failures`    | window                                              | 10m0s    |
| `reg_failures`    | failed registrations                                | 10       |
| `auth_bruteforce` | window                                              | 10m0s    |
| `auth_bruteforce` | distinct usernames from one source                  | 5        |
| `auth_bruteforce` | failures from one source (hello-sip throttle limit) | 10       |
| `trunk_down`      | down for                                            | 2m0s     |
| `trunk_asr_drop`  | window                                              | 1h0m0s   |
| `trunk_asr_drop`  | baseline                                            | 168h0m0s |
| `trunk_asr_drop`  | minimum attempts                                    | 20       |
| `trunk_asr_drop`  | status share percent                                | 30       |
| `node_health`     | not ready for                                       | 2m0s     |
| `node_health`     | config revision behind for                          | 5m0s     |
| `node_health`     | restart window                                      | 1h0m0s   |
| `node_health`     | restarts in the window                              | 3        |
| `trunk_capacity`  | percent of channels                                 | 80       |
| `trunk_capacity`  | samples at or over                                  | 3 of 5   |
| `call_quality`    | window                                              | 1h0m0s   |
| `call_quality`    | newest calls checked per trunk or node              | 5        |
| `call_quality`    | bad calls                                           | 3        |
| `call_quality`    | loss percent                                        | 1        |
| `call_quality`    | jitter ms                                           | 100      |
| `config_smells`   | device never registered after days                  | 7        |
| `config_smells`   | phone never fetched after                           | 24h0m0s  |
| `config_smells`   | phone stale after                                   | 168h0m0s |

A finding is `open`, `acknowledged` (kept listed without re-alerting), `dismissed` (not raised again for 24 hours unless its severity rises) or `resolved` (automatically, when the detector has not produced it for 30 minutes). The health score is `max(0, 10 - 3 x critical - warning)` over open findings.

## Budgets and metrics

Every model call is bounded by the concurrency, rate and timeout settings above. Tokens are summed per UTC day; background work stops at `HELLO_AI_BACKGROUND_BUDGET_PERCENT` of the budget (findings keep arriving unexplained), and an interactive request at 100 % answers `429` `ai_budget_exhausted`; waiting more than 5 s for a free slot answers `429` `ai_busy`. Metrics on `/metrics`: `hello_ai_enabled`, `hello_ai_calls_total`, `hello_ai_call_seconds`, `hello_ai_tokens_total`, `hello_ai_budget_used_ratio`, `hello_ai_busy_total`, `hello_ai_tool_calls_total`, `hello_ai_agent_runs_total`, `hello_ai_findings`, `hello_ai_proposals_total` and `hello_ai_detector_seconds`. Logs carry the feature, task id, tokens, duration and error code, never a prompt, response, tool result or key.

## Retention

Pruned hourly: tasks 24 hours; agent runs 30 days; samples 7 days; resolved or dismissed findings 30 days; terminal proposals 90 days; sessions idle for 30 days with their messages; usage rows 400 days.

## Troubleshooting by error code

The status page (`GET /api/v1/ai/status`) lists the last call errors; a failed task carries one of these codes.

| Code                                       | Meaning and remedy                                                                                                                                                                     |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ai_disabled` (503)                        | Base URL or model unset, or the endpoint is not private. Read `reason` on the status page.                                                                                             |
| `endpoint_not_private`                     | The endpoint resolves to a public address. Use a private endpoint, or opt in with `HELLO_AI_ALLOW_PUBLIC_ENDPOINT=true` knowing PBX data leaves the network.                           |
| `provider_error`                           | The endpoint refused, failed or is unreachable; the message is the provider's. Check the URL, key and that the server runs.                                                            |
| `timeout`                                  | A call exceeded `HELLO_AI_TIMEOUT`; use a faster model or raise it.                                                                                                                    |
| `invalid_output`                           | The model failed the expected structure after `HELLO_AI_VALIDATION_ATTEMPTS` tries; nothing was stored. Try `HELLO_AI_STRUCTURED_OUTPUT=prompt` for servers without `response_format`. |
| `tools_unsupported`                        | The endpoint rejects tool calls. The assistant cannot work with it; detectors and explanations (no tools) keep working. Use a model and server with tool calling.                      |
| `budget_exhausted` / `ai_budget_exhausted` | The daily token budget is spent; raise `HELLO_AI_DAILY_TOKEN_BUDGET` or wait for the next UTC day.                                                                                     |
| `ai_busy` (429)                            | All model slots stayed busy for 5 s; retry.                                                                                                                                            |
| `instance_stopped`                         | The replica running the task stopped; ask again.                                                                                                                                       |
| `canceled`                                 | The streamed answer was stopped (Stop, leaving the page, a dropped connection) before it existed; nothing was stored. Ask again.                                                       |
| `task_running` (409)                       | The session's previous message is still being answered.                                                                                                                                |
| `proposal_not_open` (409)                  | The proposal was already applied, dismissed, stale or superseded.                                                                                                                      |
