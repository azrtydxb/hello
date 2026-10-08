# Connect AI agents to Hello

Hello is an MCP server. An agent such as Claude Code connects to
`https://hello.kw.watteel.lab/mcp`, a Hello user approves what it may do,
and the agent then operates Hello through tools generated from Hello's own
API document: the same operations the console uses, with the same checks,
under the approving user's role. Unattended automation uses a
[service account](#service-accounts) instead of a person.

Values below are kw's: Hello is published at `https://hello.kw.watteel.lab`
(`HELLO_PUBLIC_URL`, the OAuth issuer and the base of both resources). The
console's **AI access** page (`/ai`) shows the same values for any
deployment, with the MCP URL to copy. Without `HELLO_PUBLIC_URL`,
`/mcp`, `/oauth/` and the metadata answer `404` and the API works with
sessions and API tokens only.

## How a client finds Hello

An MCP client needs only the URL. Everything else is discovered:

1. `POST /mcp` without a token answers `401` with
   `WWW-Authenticate: Bearer resource_metadata="https://hello.kw.watteel.lab/.well-known/oauth-protected-resource/mcp", scope="read"`.
2. That document (RFC 9728) names the resource and its authorization server,
   whose metadata is at
   `https://hello.kw.watteel.lab/.well-known/oauth-authorization-server`
   (RFC 8414): the authorize, token, revoke and registration endpoints, the
   scopes, PKCE `S256` only, and client ID metadata documents.
3. The client identifies itself by a client ID metadata document (an
   `https://` URL it publishes) or registers itself (`POST /oauth/register`,
   dynamic client registration, on for kw with `HELLO_OAUTH_DCR=true`),
   opens `/oauth/authorize` in your browser, and you approve it on Hello's
   consent screen.
4. It exchanges the code for an access token (1 h) and a refresh token, and
   calls `/mcp` with `Authorization: Bearer hello_at_…`.

The API itself has the same pair for direct OAuth clients:
`/.well-known/oauth-protected-resource/api/v1`, resource
`https://hello.kw.watteel.lab/api/v1`.

## Trust cluster-ca

kw's certificate for `hello.kw.watteel.lab` (`hello-tls`) comes from the
cluster's own `cluster-ca`, which no machine trusts out of the box, and an
MCP client that does not trust it cannot connect at all. Fetch the CA from
`http://prov.hello.kw.watteel.lab/p/ca.crt` (the provisioning host serves
it for phones, see [provisioning.md](provisioning.md#install-hellos-ca-certificate)),
check its SHA-256 fingerprint against **Phones → Settings**, and trust it:

- **macOS:** `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt`
- **Debian/Ubuntu:** copy it to `/usr/local/share/ca-certificates/kw-cluster-ca.crt`, then `sudo update-ca-certificates`
- **Node-based clients** (Claude Code, Claude Desktop's MCP runtime) also
  read `NODE_EXTRA_CA_CERTS=/path/to/ca.crt`.
- **curl:** `curl --cacert ca.crt https://hello.kw.watteel.lab/.well-known/oauth-authorization-server`

## Connect an MCP client

### Claude Code

```sh
claude mcp add --transport http hello https://hello.kw.watteel.lab/mcp
```

Then run `/mcp` in Claude Code, pick `hello` and authenticate: the browser
opens Hello's login (if you are not logged in) and the consent screen.
After you approve, `claude mcp list` shows `hello` connected and its tools
are available.

### Claude Desktop

Settings → Connectors → Add custom connector, URL
`https://hello.kw.watteel.lab/mcp`. Claude Desktop runs the same OAuth flow
in your browser.

### Any other client

Any client that speaks MCP's Streamable HTTP transport (protocol
`2026-07-28` or `2025-11-25`) and its OAuth 2.1 authorization (PKCE `S256`,
RFC 8707 `resource`, a client ID metadata document or dynamic registration)
works with the URL alone. The endpoint is stateless: `POST` only, JSON
answers, no session id. Browser-based clients must also be listed in
`HELLO_MCP_ALLOWED_ORIGINS`; any other `Origin` is refused with `403`.

## Scopes and roles

A credential carries scopes; the user behind it has a role. Every call
needs both: the role checked first (`403` `forbidden_role`), then the scope
(`403` with an `insufficient_scope` challenge, which MCP clients answer by
asking you to approve more).

| Scope     | Allows                                                                                                                                                                                            |
| --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `read`    | Every read: extensions, devices, calls, CDRs, trunks, routing, cluster health, diagnostics. The default.                                                                                          |
| `write`   | Also day-to-day configuration: extensions, devices, voicemail, recordings, announcements, ring groups, feature codes, trunks, routes, phones, templates, firmware, diagnostics.                   |
| `admin`   | Also users and roles, API tokens, service accounts, OAuth clients, settings, node drain.                                                                                                          |
| `secrets` | Operations that reveal or rotate secrets (device SIP passwords, trunk credentials). Never pre-selected; even with it, MCP results withhold secret values (see [Security model](#security-model)). |

`admin` implies `write`, which implies `read`; `secrets` stands alone.

| Role       | May grant                           |
| ---------- | ----------------------------------- |
| `viewer`   | `read`                              |
| `operator` | `read`, `write`                     |
| `admin`    | `read`, `write`, `admin`, `secrets` |

An agent never has more than its user: the role is read on every request,
so demoting a user (System → Users, admin only) narrows every app they
approved on its next call.

## Consent and revocation

The consent screen (`/oauth/consent`, never framed) shows the client's
name, its host (the name is self-asserted, the host is not; dynamically
registered clients are marked unverified), the resources it asks for and
each scope with what it allows. You may uncheck scopes; scopes your role
cannot grant are not selectable, and `secrets` is never pre-checked.

Revoke an app under **AI access → Connected apps**: its grant and every
token issued under it stop working on the next request. An `admin` sees and
revokes every user's apps. A client can also revoke its own token
(`POST /oauth/revoke`); revoking a refresh token revokes its grant. Refresh
tokens rotate on use (a reused one revokes the grant), expire after 30 days
idle (`HELLO_OAUTH_REFRESH_TTL`) and 90 days after consent
(`HELLO_OAUTH_REFRESH_MAX`).

## Service accounts

For automation without a person (CI jobs, monitoring agents), an `admin`
creates a service account under **AI access → Service accounts**, or
through the API:

```sh
curl -b cookies --json '{"name":"ci","role":"viewer","scopes":["read"]}' \
  https://hello.kw.watteel.lab/api/v1/service-accounts
curl -b cookies --json '{}' \
  https://hello.kw.watteel.lab/api/v1/service-accounts/<id>/secrets
```

The account's `id` is its OAuth client id; the secret (`hello_cs_…`) is
shown once. An account holds at most two live secrets, for rotation. It
gets an access token (1 h, no refresh token) by client credentials, with
scopes no wider than the account's:

```sh
curl -u "<id>:<secret>" -d grant_type=client_credentials -d scope=read \
  -d resource=https://hello.kw.watteel.lab/mcp \
  https://hello.kw.watteel.lab/oauth/token
```

Disabling the account or revoking a secret stops its tokens on the next
request. Its actions are audited as `service:<name>`.

## Tools and resources

Each API operation is one tool, named by its `operationId`, with the
operation's parameters (and a `body` for its JSON request body) as input;
`tools/list` shows only the tools the token's scopes allow. Read tools are
annotated read-only, `DELETE` tools destructive. Some operations are
deliberately not tools: credential, user and role management (they stay
in the console, so an agent never manages its own access), browser
sessions, file uploads and binary or CSV downloads, and the few
`secrets`-scoped operations whose whole answer is a secret. The tables are generated
by `go run ./internal/mcp/cmd/doctable` and `TestDocsAIAccess` keeps them
current.

<!-- doctable:begin -->

| Tool                        | Scope | Operation                                             | What it does                                                                                                                             |
| --------------------------- | ----- | ----------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `listAIAgents`              | read  | `GET /api/v1/ai/agents`                               | Background agents                                                                                                                        |
| `listAIFindings`            | read  | `GET /api/v1/ai/findings`                             | AIOps findings                                                                                                                           |
| `getAIFinding`              | read  | `GET /api/v1/ai/findings/{id}`                        | One finding                                                                                                                              |
| `listAIProposals`           | read  | `GET /api/v1/ai/proposals`                            | AI proposals                                                                                                                             |
| `getAIProposal`             | read  | `GET /api/v1/ai/proposals/{id}`                       | One proposal with its diff                                                                                                               |
| `listAISessions`            | read  | `GET /api/v1/ai/sessions`                             | Assistant sessions                                                                                                                       |
| `getAISession`              | read  | `GET /api/v1/ai/sessions/{id}`                        | One assistant session                                                                                                                    |
| `getAISettings`             | read  | `GET /api/v1/ai/settings`                             | External AI access settings                                                                                                              |
| `getAIStatus`               | read  | `GET /api/v1/ai/status`                               | AI agent status                                                                                                                          |
| `getAITask`                 | read  | `GET /api/v1/ai/tasks/{id}`                           | An AI task                                                                                                                               |
| `listAnnouncements`         | read  | `GET /api/v1/announcements`                           | List announcements                                                                                                                       |
| `deleteAnnouncement`        | write | `DELETE /api/v1/announcements/{id}`                   | Delete an announcement                                                                                                                   |
| `getMe`                     | read  | `GET /api/v1/auth/me`                                 | The authenticated user                                                                                                                   |
| `listCalls`                 | read  | `GET /api/v1/calls`                                   | Every active call in the cluster (from Valkey)                                                                                           |
| `listCDRs`                  | read  | `GET /api/v1/cdrs`                                    | Call detail records, newest first                                                                                                        |
| `cdrConcurrency`            | read  | `GET /api/v1/cdrs/concurrency`                        | Recorded calls in progress over a time window, by direction                                                                              |
| `countCDRs`                 | read  | `GET /api/v1/cdrs/counts`                             | How many call records exist, and how many failed                                                                                         |
| `getCDR`                    | read  | `GET /api/v1/cdrs/{id}`                               | One CDR with its routing trace and failure explanation                                                                                   |
| `getCluster`                | read  | `GET /api/v1/cluster`                                 | Cluster members, dependency health and configuration revision                                                                            |
| `listClusterNodes`          | read  | `GET /api/v1/cluster/nodes`                           | Every member, live and OFFLINE                                                                                                           |
| `drainNode`                 | admin | `POST /api/v1/cluster/nodes/{id}/drain`               | Ask a node to drain                                                                                                                      |
| `undrainNode`               | admin | `DELETE /api/v1/cluster/nodes/{id}/drain`             | Cancel a drain request                                                                                                                   |
| `listDevices`               | read  | `GET /api/v1/devices`                                 | Every device, ordered by SIP username                                                                                                    |
| `createDevice`              | write | `POST /api/v1/devices`                                | Create a device; its SIP secret is returned only in this response                                                                        |
| `getDevice`                 | read  | `GET /api/v1/devices/{id}`                            | One device (never its secret)                                                                                                            |
| `updateDevice`              | write | `PATCH /api/v1/devices/{id}`                          | Enable, disable or move a device                                                                                                         |
| `deleteDevice`              | write | `DELETE /api/v1/devices/{id}`                         | Delete a device                                                                                                                          |
| `listAuthFailures`          | read  | `GET /api/v1/diagnostics/auth-failures`               | Source IPs with failed authentications                                                                                                   |
| `clearAuthFailures`         | write | `DELETE /api/v1/diagnostics/auth-failures/{ip}`       | Unblock a source IP                                                                                                                      |
| `getDeviceDiagnostics`      | read  | `GET /api/v1/diagnostics/devices/{id}`                | Why a device is or is not registered                                                                                                     |
| `listExtensions`            | read  | `GET /api/v1/extensions`                              | Every extension, ordered by number                                                                                                       |
| `createExtension`           | write | `POST /api/v1/extensions`                             | Create an extension                                                                                                                      |
| `getExtension`              | read  | `GET /api/v1/extensions/{id}`                         | One extension                                                                                                                            |
| `updateExtension`           | write | `PATCH /api/v1/extensions/{id}`                       | Change an extension's number or name                                                                                                     |
| `deleteExtension`           | write | `DELETE /api/v1/extensions/{id}`                      | Delete an extension and its devices                                                                                                      |
| `getExtensionVoicemail`     | read  | `GET /api/v1/extensions/{id}/voicemail`               | One extension's voicemail box                                                                                                            |
| `updateExtensionVoicemail`  | write | `PUT /api/v1/extensions/{id}/voicemail`               | Change a voicemail box                                                                                                                   |
| `listFeatureCodes`          | read  | `GET /api/v1/feature-codes`                           | List DTMF feature codes                                                                                                                  |
| `putFeatureCodes`           | write | `PUT /api/v1/feature-codes`                           | Replace the DTMF feature codes                                                                                                           |
| `listPhones`                | read  | `GET /api/v1/phones`                                  | Every provisioned phone, ordered by MAC                                                                                                  |
| `createPhone`               | write | `POST /api/v1/phones`                                 | Add a phone; binding an existing device rotates its secret. The provisioning URL with the token is returned only here                    |
| `getPhone`                  | read  | `GET /api/v1/phones/{id}`                             | One phone                                                                                                                                |
| `updatePhone`               | write | `PATCH /api/v1/phones/{id}`                           | Change a phone; deviceId null unbinds (and disables) it, a new deviceId or extensionId rebinds it and rotates that device's secret       |
| `deletePhone`               | write | `DELETE /api/v1/phones/{id}`                          | Delete a phone; its device stays without the sealed secret                                                                               |
| `listPhoneFetches`          | read  | `GET /api/v1/phones/{id}/fetches`                     | The phone's provisioning fetches, newest first                                                                                           |
| `previewPhoneFile`          | read  | `GET /api/v1/phones/{id}/preview`                     | Render a file as the phone would get it, with the SIP secret, admin password and token masked; not a fetch                               |
| `listPresence`              | read  | `GET /api/v1/presence`                                | Device presence (BLF states)                                                                                                             |
| `listFirmware`              | read  | `GET /api/v1/prov/firmware`                           | Uploaded firmware files                                                                                                                  |
| `putFirmwarePins`           | write | `PUT /api/v1/prov/firmware/pins`                      | Replace every firmware pin                                                                                                               |
| `deleteFirmware`            | write | `DELETE /api/v1/prov/firmware/{id}`                   | Delete a firmware file; refused while pinned                                                                                             |
| `listRedirectAccounts`      | admin | `GET /api/v1/prov/redirect`                           | The vendor redirect-service accounts                                                                                                     |
| `putRedirectAccount`        | admin | `PUT /api/v1/prov/redirect/{vendor}`                  | Set a vendor's account; credentials are checked with the vendor, sealed and never returned. Accounts set by the deployment are read-only |
| `deleteRedirectAccount`     | admin | `DELETE /api/v1/prov/redirect/{vendor}`               | Remove a vendor's stored account                                                                                                         |
| `checkRedirectAccount`      | admin | `POST /api/v1/prov/redirect/{vendor}/check`           | Test the vendor's effective credentials                                                                                                  |
| `getProvSettings`           | read  | `GET /api/v1/prov/settings`                           | Computed provisioning URLs, CA fingerprint and DHCP option values                                                                        |
| `listProvTemplates`         | read  | `GET /api/v1/prov/templates`                          | Built-in and stored provisioning templates                                                                                               |
| `createProvTemplate`        | write | `POST /api/v1/prov/templates`                         | Create a template                                                                                                                        |
| `validateProvTemplate`      | write | `POST /api/v1/prov/templates/validate`                | Validate a template without saving it                                                                                                    |
| `getProvTemplate`           | read  | `GET /api/v1/prov/templates/{id}`                     | One template                                                                                                                             |
| `updateProvTemplate`        | write | `PATCH /api/v1/prov/templates/{id}`                   | Change a stored template; the previous version is kept                                                                                   |
| `deleteProvTemplate`        | write | `DELETE /api/v1/prov/templates/{id}`                  | Delete a stored template; refused while a phone overrides with it                                                                        |
| `copyProvTemplate`          | write | `POST /api/v1/prov/templates/{id}/copy`               | Copy a template (built-in or stored) to edit, one priority higher                                                                        |
| `listRecordings`            | read  | `GET /api/v1/recordings`                              | List call recordings                                                                                                                     |
| `deleteRecording`           | write | `DELETE /api/v1/recordings/{id}`                      | Delete a recording                                                                                                                       |
| `listRegistrations`         | read  | `GET /api/v1/registrations`                           | Every registered contact binding in the cluster (from Valkey)                                                                            |
| `listRingGroups`            | read  | `GET /api/v1/ring-groups`                             | List ring/hunt groups                                                                                                                    |
| `createRingGroup`           | write | `POST /api/v1/ring-groups`                            | Create a ring/hunt group                                                                                                                 |
| `getRingGroup`              | read  | `GET /api/v1/ring-groups/{id}`                        | One ring/hunt group                                                                                                                      |
| `updateRingGroup`           | write | `PATCH /api/v1/ring-groups/{id}`                      | Change a ring/hunt group                                                                                                                 |
| `deleteRingGroup`           | write | `DELETE /api/v1/ring-groups/{id}`                     | Delete a ring/hunt group                                                                                                                 |
| `listInboundRoutes`         | read  | `GET /api/v1/routes/inbound`                          | Every inbound route in position order                                                                                                    |
| `createInboundRoute`        | write | `POST /api/v1/routes/inbound`                         | Append an inbound route                                                                                                                  |
| `reorderInboundRoutes`      | write | `PUT /api/v1/routes/inbound/order`                    | Reorder every inbound route at once                                                                                                      |
| `getInboundRoute`           | read  | `GET /api/v1/routes/inbound/{id}`                     | One inbound route                                                                                                                        |
| `updateInboundRoute`        | write | `PATCH /api/v1/routes/inbound/{id}`                   | Change an inbound route                                                                                                                  |
| `deleteInboundRoute`        | write | `DELETE /api/v1/routes/inbound/{id}`                  | Delete an inbound route                                                                                                                  |
| `listOutboundRoutes`        | read  | `GET /api/v1/routes/outbound`                         | Every outbound route in position order                                                                                                   |
| `createOutboundRoute`       | write | `POST /api/v1/routes/outbound`                        | Append an outbound route                                                                                                                 |
| `reorderOutboundRoutes`     | write | `PUT /api/v1/routes/outbound/order`                   | Reorder every outbound route at once                                                                                                     |
| `getOutboundRoute`          | read  | `GET /api/v1/routes/outbound/{id}`                    | One outbound route                                                                                                                       |
| `updateOutboundRoute`       | write | `PATCH /api/v1/routes/outbound/{id}`                  | Change an outbound route                                                                                                                 |
| `deleteOutboundRoute`       | write | `DELETE /api/v1/routes/outbound/{id}`                 | Delete an outbound route                                                                                                                 |
| `testRouting`               | write | `POST /api/v1/routing/test`                           | Decide a call against the saved configuration without placing it                                                                         |
| `listSkills`                | read  | `GET /api/v1/skills`                                  | The agent skills this Hello ships                                                                                                        |
| `listTrunks`                | read  | `GET /api/v1/trunks`                                  | Every trunk (never its password)                                                                                                         |
| `createTrunk`               | write | `POST /api/v1/trunks`                                 | Create a trunk                                                                                                                           |
| `listTrunkStatus`           | read  | `GET /api/v1/trunks/status`                           | Live registration, destination health and active calls of every trunk                                                                    |
| `getTrunk`                  | read  | `GET /api/v1/trunks/{id}`                             | One trunk (never its password)                                                                                                           |
| `updateTrunk`               | write | `PATCH /api/v1/trunks/{id}`                           | Change a trunk                                                                                                                           |
| `deleteTrunk`               | write | `DELETE /api/v1/trunks/{id}`                          | Delete a trunk                                                                                                                           |
| `getVersion`                |       | `GET /api/v1/version`                                 | Build and configuration revision of this control-plane node                                                                              |
| `listVoiceAgents`           | read  | `GET /api/v1/voice/agents`                            | List voice agents                                                                                                                        |
| `createVoiceAgent`          | write | `POST /api/v1/voice/agents`                           | Create a voice agent                                                                                                                     |
| `getVoiceAgent`             | read  | `GET /api/v1/voice/agents/{id}`                       | Show a voice agent                                                                                                                       |
| `updateVoiceAgent`          | write | `PUT /api/v1/voice/agents/{id}`                       | Update a voice agent                                                                                                                     |
| `deleteVoiceAgent`          | write | `DELETE /api/v1/voice/agents/{id}`                    | Delete a voice agent                                                                                                                     |
| `listVoiceAgentCalls`       | read  | `GET /api/v1/voice/agents/{id}/calls`                 | List a voice agent's calls                                                                                                               |
| `putVoiceAgentTools`        | write | `PUT /api/v1/voice/agents/{id}/tools`                 | Set a voice agent's MCP tools                                                                                                            |
| `listVoiceAgentVersions`    | read  | `GET /api/v1/voice/agents/{id}/versions`              | List a voice agent's versions                                                                                                            |
| `restoreVoiceAgentVersion`  | write | `POST /api/v1/voice/agents/{id}/versions/{v}/restore` | Restore a voice agent version                                                                                                            |
| `listVoiceMCPServers`       | read  | `GET /api/v1/voice/mcp-servers`                       | List MCP servers                                                                                                                         |
| `getVoiceMCPServer`         | read  | `GET /api/v1/voice/mcp-servers/{id}`                  | Show an MCP server                                                                                                                       |
| `getVoiceStatus`            | read  | `GET /api/v1/voice/status`                            | Show the voice runtime status                                                                                                            |
| `listVoicemailBoxes`        | read  | `GET /api/v1/voicemail/boxes`                         | List voicemail boxes                                                                                                                     |
| `listVoicemailMessages`     | read  | `GET /api/v1/voicemail/messages`                      | List a box's voicemail messages                                                                                                          |
| `deleteVoicemailMessage`    | write | `DELETE /api/v1/voicemail/messages/{id}`              | Delete a voicemail message                                                                                                               |
| `markVoicemailMessageHeard` | write | `POST /api/v1/voicemail/messages/{id}/heard`          | Mark a voicemail message heard                                                                                                           |

| Resource                           | Scope | What it holds                                    |
| ---------------------------------- | ----- | ------------------------------------------------ |
| `hello://calls`                    | read  | The calls in progress across the cluster.        |
| `hello://registrations`            | read  | The devices registered now, with their contacts. |
| `hello://cdrs/recent`              | read  | The last 50 call detail records.                 |
| `hello://trunks/status`            | read  | Each trunk's registration and reachability.      |
| `hello://cluster`                  | read  | The cluster's nodes, leases and health.          |
| `hello://cdrs/{id}`                | read  | One CDR with its trace.                          |
| `hello://extensions/{id}`          | read  | One extension.                                   |
| `hello://diagnostics/devices/{id}` | read  | A device's registration attempts and failures.   |

<!-- doctable:end -->

The server also offers three prompts that start common work from these
tools and resources: `troubleshoot-call`, `onboard-user` and
`review-routing`.

## Install the skills

Hello ships agent skills (agentskills.io format) that teach an agent its
workflows on top of the tools: `hello-setup` (users: extensions, SIP
devices, voicemail boxes and auto-provisioned desk phones),
`hello-routing` (trunks, inbound and outbound routes, route order, ring
groups) and `hello-troubleshoot` (a call that failed, a phone that does
not register). Download them under **AI access → Agent skills**, or with
`GET /api/v1/skills/{name}/download` (a zip), and unpack each into:

- **Claude Code:** `~/.claude/skills/` (or `.claude/skills/` in a project).
- **Claude Desktop:** Settings → Capabilities → Skills → upload the zip.
- **Other clients:** their skills folder; the zip is the skill folder itself.

The sources are in [skills/](../skills/); a test fails if a skill names a
tool that does not exist.

## Security model

- **Same API, same checks.** A tool call is replayed in process through
  Hello's API with the caller's own token: role, scope, validation and
  audit are exactly the console's. Tokens are bound to their resource
  (`/mcp` or `/api/v1`); an MCP token is refused on the API directly.
- **Secrets withheld.** Values the API marks `x-hello-secret` (a new
  device's SIP password, a revealed trunk credential) are replaced in MCP
  results by `[withheld: shown only in the Hello console]`, even with the
  `secrets` scope. Read them in the console.
- **Audit `via`.** Every change made through an OAuth token is audited
  under the approving user, with the OAuth client id in the row's `via`;
  service accounts appear as `service:<name>`. MCP reads are logged and
  counted (`hello_mcp_requests_total`, `hello_mcp_tool_calls_total`).
- **No credential at rest or in logs.** Tokens, codes and client secrets
  are stored only as hashes, carry recognisable prefixes (`hello_at_`,
  `hello_rt_`, `hello_cs_`, `hello_pat_`) for secret scanners and log
  redaction, and never appear in logs, metrics or audit rows.
- **OAuth 2.1 only.** PKCE `S256` is required, redirect URIs match exactly
  (any port on a loopback address), codes are single use for 60 seconds,
  every authorization response carries `iss`, token responses are
  `Cache-Control: no-store`, and failed client authentication is throttled.
- **Limits.** An approved agent can do anything its user's role and the
  granted scopes allow, including `write` changes; grant `read` unless the
  agent needs more, and revoke apps you no longer use.
