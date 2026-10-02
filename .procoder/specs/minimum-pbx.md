# minimum-pbx

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 1 — Minimum PBX, plus §8 (registrar),
§9 (extensions and devices), §10 (security), §22 (CDRs). Decided 2026-10-02
(`.procoder/ask/answers.md`): B2BUA call model with SDP passed through and
direct media; registrations in Valkey from this phase; local users with
sessions and API tokens; automated SIP tests use Go test user agents built on
sipgo.

## Problem

Phase 0 left Hello with healthy, observable services that do no telephony. An
administrator cannot create an extension, a phone cannot register, and two
phones cannot call each other — the minimum anyone means by "a PBX". Phase 1
delivers exactly that over SIP/UDP, on the two-node lab, with registrations
already shared cluster-wide so the Phase 3 HA work extends this design instead
of replacing it.

## Users

- **Administrators** — create extensions and devices through the API or UI,
  receive each device's SIP secret once, and see who is registered and who is
  on a call.
- **Phone users** — register a desk phone or softphone with the credentials
  the administrator gave them, dial another extension's number, and talk.
- **Operators** — read CDRs, metrics, and logs to understand what happened to
  a call, without secrets appearing anywhere.
- **Developers** — run the whole flow (register, call, hang up) as automated
  tests against the compose lab.

## In scope

- [S-1] hello-sip listens for SIP over UDP on `HELLO_SIP_BIND_ADDR` using `emiago/sipgo` behind a Hello-owned package in `internal/sip/`, writes `HELLO_SIP_ADVERTISED_ADDR` into Via/Contact/Record-Route, and answers OPTIONS with 200.
- [S-2] Local management users: a first admin is created from `HELLO_BOOTSTRAP_ADMIN_PASSWORD` when the users table is empty; passwords stored as bcrypt; `POST /api/v1/auth/login` issues an HttpOnly, SameSite=Strict session cookie; `POST /api/v1/tokens` issues bearer API tokens stored only as SHA-256 hashes; every `/api/v1` route except version, openapi, and login requires one of the two.
- [S-3] Extensions and devices in PostgreSQL with CRUD at `/api/v1/extensions` and `/api/v1/devices`: an extension has a number (2–10 digits, unique) and a display name; a device belongs to one extension and has a SIP username (unique), an enabled flag, and a generated secret returned only in the create and rotate-secret responses. Only digest HA1 values (MD5 and SHA-256) are stored, never the secret.
- [S-4] Every configuration change is validated, writes an audit event (actor, action, resource, time), and bumps the configuration revision in the `schema_info` table (`migrations/00001_schema_info.sql`) in the same transaction; `/api/v1/version` reports the real revision.
- [S-5] hello-sip holds an in-memory snapshot of enabled extensions and devices, tagged with its revision, loaded from PostgreSQL at startup and reloaded on a PostgreSQL `NOTIFY` after each change plus a periodic poll; no database call happens while handling a SIP request.
- [S-6] REGISTER with digest authentication (RFC 3261 MD5 and RFC 8760 SHA-256) using stateless HMAC nonces signed with `HELLO_SIP_NONCE_SECRET`, so any node validates a challenge another node issued; expiry clamped to configured bounds; `Expires: 0` and `Contact: *` unregister; multiple contacts per AOR.
- [S-7] Contact bindings live in Valkey, one hash per AOR, with the fields from spec §8 (contact URI, source address and port, transport, expiry, User-Agent, Path, receiving node, last update) and per-binding expiry.
- [S-8] Failed-authentication throttling: after `HELLO_SIP_AUTH_FAIL_LIMIT` failures from one source IP within a window, that IP gets 403 without a challenge until the window passes; counters are in Valkey so the limit holds across nodes.
- [S-9] Internal calls as a B2BUA: an INVITE authenticated by digest from a device, to an extension number, is forked to every registered contact of that extension (ring all); the first 2xx wins and the other forks are cancelled; ACK, BYE from either side, caller CANCEL, and in-dialog re-INVITE/UPDATE are relayed; SDP passes through unmodified. A binding registered through another node is reached through that node: the registrar stores a Path URI with an HMAC-signed flow token, the calling node sends the INVITE via that Path, and the registering node relays it over the phone's existing flow as an edge proxy, record-routing itself so in-dialog requests take the same path (decided 2026-10-02).
- [S-10] Call failure mapping: unknown number → 404, extension with no registrations → 480, every fork busy → 486, no answer within `HELLO_SIP_RING_TIMEOUT` → 408 to the caller and CANCEL to the forks.
- [S-11] Live state API: `GET /api/v1/registrations` lists bindings from Valkey; `GET /api/v1/calls` lists active calls, which each SIP node publishes to Valkey with a heartbeat TTL so a dead node's calls disappear.
- [S-12] CDRs: when a call ends, the SIP node queues a CDR (fields from spec §22 that apply without trunks or routing) and a background writer inserts it into PostgreSQL outside the SIP path; `GET /api/v1/cdrs` pages through them newest first.
- [S-13] Metrics: `hello_sip_registrations`, `hello_active_calls`, `hello_calls_total{result}`, `hello_sip_requests_total{method}`, `hello_sip_responses_total{code}`, `hello_cdr_dropped_total`.
- [S-14] UI: login page; Extensions and Devices pages with create, edit, delete, and rotate secret (the secret shown once with a copy button); Registrations, Active Calls, and Call History pages reading the live APIs.
- [S-15] Lab: hello-sip-1 and hello-sip-2 expose SIP/UDP on host ports 5060 and 5062; a new phone-setup guide under docs/ explains how to point two physical phones at the lab.
- [S-16] Test user agents in `test/sipua/` built on sipgo, and lab integration tests driving REGISTER and call flows through both SIP nodes.

## Out of scope

- SIP over TCP and TLS, WebSocket — later phases.
- Trunks, inbound/outbound routes, number rewriting, routing traces — Phase 2.
- Shared dialog state and mid-dialog failover; a call stays on the node that set it up — Phase 3/7.
- Ring and hunt group strategies beyond ring-all to one extension's own devices, forwarding, DND, transfer, call pickup, voicemail, presence/BLF — Phase 4.
- Media anchoring, RTP relay, NAT traversal beyond `rport`/`received` — Phase 5.
- Roles/RBAC, OIDC, LDAP — later; every Phase 1 user is an administrator.
- REGISTER and INVITE rate limiting beyond failed-auth throttling.
- Per-device codec, transport, source-IP, and caller-ID policies.

## Constraints

- No PostgreSQL call on the SIP request path; Valkey calls on it carry a timeout of `HELLO_SIP_STATE_TIMEOUT` (default 200ms).
- REGISTER handled in under 20ms p99 on the lab with Valkey local (measured by a benchmark, not asserted in CI).
- Secrets — device SIP secrets, user passwords, API tokens, `HELLO_SIP_NONCE_SECRET` — never appear in logs, API responses after creation, metrics, or CDRs; Authorization headers are stripped from any logged SIP message (verified by `procoder security` and the redaction tests).
- New dependencies limited to `emiago/sipgo` and `golang.org/x/crypto` (bcrypt).
- UDP only; SIP messages larger than the path MTU are not split onto TCP in this phase.

## Interfaces

- Env (sip): `HELLO_SIP_NONCE_SECRET` (required, at least 32 bytes), `HELLO_SIP_DOMAIN` (required; the digest realm and AOR host, identical on every node and on hello-control — the lab's two nodes advertise different hosts, so it cannot default to one), `HELLO_SIP_AUTH_FAIL_WINDOW` (5m), `HELLO_SIP_REGISTER_MIN_EXPIRES` (60s), `HELLO_SIP_REGISTER_MAX_EXPIRES` (3600s), `HELLO_SIP_RING_TIMEOUT` (30s), `HELLO_SIP_AUTH_FAIL_LIMIT` (10 per 5 minutes), `HELLO_SIP_STATE_TIMEOUT` (200ms), `HELLO_DATABASE_URL` (now also required by hello-sip, read-only use).
- Env (control): `HELLO_SIP_DOMAIN` (required, used to compute device HA1 values), `HELLO_VALKEY_ADDR` (required, read for the live views), `HELLO_BOOTSTRAP_ADMIN_PASSWORD`, used only when no user exists; `HELLO_SESSION_TTL` (12h).
- SIP: REGISTER, OPTIONS, INVITE, ACK, CANCEL, BYE, re-INVITE, UPDATE on UDP.
- HTTP: `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`, `GET|POST /api/v1/tokens`, `DELETE /api/v1/tokens/{id}`, `GET|POST /api/v1/extensions`, `GET|PATCH|DELETE /api/v1/extensions/{id}`, `GET|POST /api/v1/devices`, `GET|PATCH|DELETE /api/v1/devices/{id}`, `POST /api/v1/devices/{id}/rotate-secret`, `GET /api/v1/registrations`, `GET /api/v1/calls`, `GET /api/v1/cdrs?before=&limit=`. All described in the OpenAPI document.
- UI routes: `/login`, `/extensions`, `/devices`, `/registrations`, `/calls`, `/history`.

## Data

- PostgreSQL (owned by hello-control; hello-sip reads extensions and devices and inserts CDRs):
  - `users` (id, username, bcrypt hash, created_at)
  - `sessions` (id hash, user_id, expires_at)
  - `api_tokens` (id, user_id, name, SHA-256 hash, created_at, last_used_at)
  - `extensions` (id, number unique, name, timestamps)
  - `devices` (id, extension_id, sip_username unique, ha1_md5, ha1_sha256, enabled, timestamps)
  - `audit_events` (id, actor, action, resource, resource_id, at)
  - `cdrs` (id, correlation_id, sip_call_id, source, destination, start/ring/answer/end times, duration, billable duration, sip_node, media_mode, final_status, termination_side, failure_reason)
- Valkey (written by hello-sip):
  - `hello:reg:{aor}` hash of bindings, each with its own expiry
  - `hello:call:{id}` active-call record, TTL refreshed by the owning node
  - `hello:authfail:{ip}` counter with window TTL
- hello-sip memory: the configuration snapshot and the dialogs it owns.

## Edge cases

- A REGISTER refresh from the same Contact on a different node must update the binding, not add a second one.
- Two devices on one extension, one registered twice (two Contacts) — ring all three, cancel the losers, and do not leave a fork ringing after the winner answers.
- 200 OK from two forks at once: the B2BUA accepts the first, sends ACK then BYE to the second.
- Caller CANCEL racing the callee's 200 OK: the call ends cleanly, with BYE sent to the answering leg and a CDR recording a cancelled call.
- A nonce issued by hello-sip-1 presented to hello-sip-2; an expired nonce gets 401 with `stale=true`, not 403.
- A device disabled or deleted while registered: its next REGISTER or INVITE is rejected, and its existing bindings are removed on the next snapshot reload.
- An extension calling itself — ring its other devices, not the calling device.
- An unspecified `Expires`, or one outside the bounds: below the minimum → 423 with `Min-Expires`, above the maximum → clamp.
- Malformed SIP, or an oversized or binary UDP datagram — dropped or answered 400, never a crash.
- A PostgreSQL `NOTIFY` lost during a reconnect — the periodic poll catches the revision gap.

## Failure modes

- **Valkey unavailable:** REGISTER gets 503 with Retry-After, INVITE gets 503, `/readyz` fails so traffic moves to a node that can reach Valkey; existing dialogs continue.
- **PostgreSQL unavailable while hello-sip runs:** it keeps serving from its last snapshot (HA Level 1) and logs and counts reload failures. CDRs buffer in memory up to a bound, then are dropped and counted in `hello_cdr_dropped_total`.
- **PostgreSQL unavailable at hello-sip startup:** no snapshot means `/readyz` fails until the first load succeeds.
- **hello-control down:** registration and calling are unaffected; only management and the live views are unavailable.
- **A SIP node dies mid-call:** its calls disappear from `/api/v1/calls` when their TTL expires; the media may continue but signaling for those dialogs is lost (stated limitation until Phase 7).
- **A wrong `HELLO_SIP_NONCE_SECRET` on one node:** that node rejects challenges the other issued; it is detectable because test UA authentication through mixed nodes fails, and it is documented in the README.

## Acceptance criteria

- [ ] [S-1] `TestOptionsPing` in `internal/sip` passes — fails if an OPTIONS request is not answered 200 with the advertised address in the Via/Contact of the response.
- [ ] [S-2] `TestAuthRequired`, `TestLoginSession` and `TestAPITokenHashed` in `internal/api` pass — fail if a protected route answers without a session or token, if the cookie lacks HttpOnly or SameSite=Strict, or if the token's plaintext is found in the database.
- [ ] [S-3] `TestDeviceSecretShownOnce` in `internal/api` passes — fails if the secret appears in a GET response, if it is stored instead of HA1 values, or if a duplicate extension number or SIP username is accepted.
- [ ] [S-4] `TestConfigChangeAuditedAndRevisioned` in `test/integration` passes — fails if creating, updating or deleting an extension does not write an audit row and increase `config_revision` exactly once.
- [ ] [S-5] `TestSnapshotReloadOnNotify` and `TestSnapshotSurvivesDatabaseLoss` in `test/integration` pass — fail if hello-sip does not see a new device within 2s of its creation, or stops authenticating known devices when PostgreSQL is stopped.
- [ ] [S-6] `TestDigestMD5AndSHA256`, `TestNonceAcrossNodes` and `TestStaleNonce` in `internal/sip` pass — fail if either algorithm is rejected for a correct password or accepted for a wrong one, if a nonce from another node with the same secret fails, or if an expired nonce is not answered with `stale=true`.
- [ ] [S-7] `TestRegisterBindings` in `test/integration` passes — fails if two contacts for one AOR are not both stored, if a refresh through the other node duplicates a binding, or if `Expires: 0` leaves a binding behind.
- [ ] [S-8] `TestAuthFailThrottle` in `test/integration` passes — fails if the eleventh bad attempt from one IP, sent alternately to both nodes, is challenged instead of rejected with 403.
- [ ] [S-9] `TestCallRingAllAndHangup` in `test/integration` passes — fails if a call to an extension with two registered test UAs does not ring both, if the losing UA keeps ringing after the other answers, if SDP differs end to end, or if BYE from either side does not end both legs.
- [ ] [S-9] `TestCallAcrossNodes` in `test/integration` passes — fails if a UA registered through hello-sip-1 cannot be called by a UA registered through hello-sip-2.
- [ ] [S-10] `TestCallFailureCodes` in `test/integration` passes — fails if an unknown number, an unregistered extension, a busy callee, or a ring timeout produces a code other than 404, 480, 486 or 408 respectively.
- [ ] [S-11] `TestLiveRegistrationsAndCalls` in `test/integration` passes — fails if an active call or registration is missing from the API while it exists, or still listed 30s after its node is killed.
- [ ] [S-12] `TestCDRWritten` in `test/integration` passes — fails if an answered and a cancelled call do not each produce one CDR with correct answer time, billable duration and termination side.
- [ ] [S-13] `TestSIPMetrics` in `internal/sip` passes — fails if a REGISTER and a completed call do not move `hello_sip_requests_total`, `hello_sip_responses_total`, `hello_calls_total` and `hello_sip_registrations`.
- [ ] [S-14] `procoder test` and `procoder lint` pass over `web/`; `Devices.test.tsx` fails if the secret is not shown after creation or is still shown after navigating away; `Login.test.tsx` fails if an unauthenticated API response does not redirect to `/login`.
- [ ] [S-15] [S-16] `TestLabSmoke` in `test/integration` (`HELLO_DOCKER=1`) registers two test UAs through host ports 5060 and 5062 and completes a call between them — fails if either UA cannot register or the call does not complete; the phone-setup guide in docs/ documents the physical-phone steps.
- [ ] [S-16] `TestNoSecretsInLogs` in `test/integration` passes — fails if a device secret, password, API token or Authorization header value appears in any service's log output during the lab call flow.

## Open questions

<!-- All resolved 2026-10-02; answers in .procoder/ask/answers.md and folded into the Source line, Constraints, and In scope. -->
