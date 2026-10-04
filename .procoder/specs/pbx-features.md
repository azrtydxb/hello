# pbx-features

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 4 (PBX Features), §14 (ring and hunt groups), §15 (call features). Decided 2026-10-04 (`.procoder/ask/answers.md`): voicemail audio in MinIO (S3-compatible) in the hello namespace; voicemail delivery via web, MWI **and** SMTP email with the audio attached; ring/hunt groups ship **all five strategies** (ring-all, sequential, round-robin, longest-idle, weighted).

## Problem

After Phase 3 Hello connects phones to each other and to carriers, but it is not yet a PBX people can live on: a call that isn't answered dies, there is no voicemail, no hold, no transfer, no way to send calls away when you're busy, no ring groups for a team, and no BLF lamps showing who is on a call. Phase 4 adds the day-to-day call features on top of the Phase 1–3 call engine.

## Users

- **Phone users:** let an unanswered call go to voicemail and hear it later from the phone or the UI; hold and transfer calls; forward their extension; toggle DND; see colleagues' line states on BLF keys; dial a group and have it ring the right people in the right order.
- **Administrators:** create ring/hunt groups, voicemail boxes and per-extension settings (forwarding targets, DND, email address) in the UI and API.
- **Operators:** see voicemail storage usage and email delivery results.

## In scope

- [S-1] **Call hold:** re-INVITE with sendonly SDP to both legs on hold start, sendrecv on resume. Hold is per call, signalled by the phone (sendonly offer), relayed by the B2BUA; a held call's CDR is unchanged.
- [S-2] **Blind transfer:** the phone sends REFER; Hello terminates its leg and originates a new call from the transferee to the Refer-To target, with the original caller kept as the remote party; the transferee's phone is told via NOTIFY whether the transfer succeeded. Refer-To may be an extension, an external number (routed through outbound rules) or a SIP URI.
- [S-3] **Attended transfer:** the transferee puts the first call on hold, calls the target as a normal call, bridges the two existing calls with a REFER (Replace) — after which both original parties are connected and the transferee's legs drop. The bridge keeps both original Call-IDs' CDRs and opens a bridged-transfer link between them.
- [S-4] **Call forwarding:** per extension, `always`, `busy` and `no-answer` targets (disabled by default). Evaluated in the B2BUA: always before ringing; busy after a 486/600; no-answer after `HELLO_SIP_RING_TIMEOUT`. Forwarding loops are detected (a chain that returns to a visited extension rejects with 408 "forward loop"). Configured via `PATCH /api/v1/extensions/{id}` and the phone's feature codes.
- [S-5] **DND:** per extension. A DND extension rejects incoming calls with 603 immediately, sends them to voicemail if a box exists, and stays reachable for internal DND-bypass (ring groups may ignore DND per their configuration). Toggled via the API and `*78`/`*79`.
- [S-6] **Ring groups:** a named group with members (extensions, each with a per-member delay), a strategy, ring timeout, and a failure destination (voicemail box, external number or hangup). Strategies: **ring-all** (existing fork behaviour, plus DND handling), **sequential** (members in order, each until its per-member delay), **round-robin** (start position rotates per call), **longest-idle** (member with the oldest last-call-end time first — idle state comes from the Phase 3 livestate), **weighted** (distribution by member weight, deterministic per call like Phase 2's destination weighting).
- [S-7] **Hunt groups:** the same group object with `hunt: true` — calls move to the next member on no-answer or busy and never ring two members at once; the failure destination applies after the last member.
- [S-8] **Voicemail:** a box per extension (created automatically with the extension, deletable), greeting (uploaded via the API) and unreachable greeting; unanswered/busy/DND calls with a box go to voicemail after the ring timeout (busy/DND immediately). The greeting plays, then the beep, then recording; the caller can press `#` to finish or `*` to retry. Messages are stored: audio (PCM WAV, PCMA/PCMU as recorded) in MinIO, metadata in PostgreSQL. MWI: MESSAGE with `Message-Account` and counts after each change. Email: per-box address, SMTP delivery with the audio attached and a text summary, retried 3× with backoff; delivery result recorded on the message.
- [S-9] **Voicemail retrieval:** `*97` (own box, prompts for password) or dialing the box's retrieval number; UI page listing messages (duration, date, heard flag), browser playback via presigned MinIO URLs, mark heard/unheard, delete (removes MinIO object + row). Password per box, stored as a hash.
- [S-10] **Presence/BLF:** dialog state per device (idle, ringing, on-call, DND) in the Phase 3 livestate, published by the owning node; phones SUBSCRIBE to `BLF` event-targets (extension numbers) and Hello answers with NOTIFY dialog state on every change. SUBSCRIBE authentication follows Phase 1 digest; subscriptions expire with their expiry and are re-negotiated.
- [S-11] **Feature codes** (DTMF, stored in the database and configurable through the API; these are the defaults): `*72`/`*73` forward-always set/clear, `*90`/`*91` forward-busy, `*92`/`*93` forward-no-answer, `*78`/`*79` DND on/off, `*97` voicemail, `##` attended-transfer target prompt, `*2` blind transfer (DTMF-based transfer requires the `REFER` method fallback above too).
- [S-12] **MinIO in the hello namespace:** a MinIO deployment + Service + bucket `hello-voicemail` added to `deploy/kuvryn-sync/kw/` plus a local-dev equivalent in compose (the MinIO access keys are handled like every other deployment secret — see Constraints).
- [S-13] **Metrics:** `hello_voicemail_messages_total{result}`, `hello_voicemail_storage_bytes`, `hello_voicemail_email_total{result}`, `hello_transfers_total{kind,result}`, `hello_forwarded_calls_total{kind}`, `hello_group_calls_total{group,strategy,result}`, `hello_presence_subscriptions`, `hello_hold_active`.
- [S-14] **UI:** voicemail page (list, play, heard/delete, per-box settings incl. email and password), ring/hunt groups page (CRUD, strategy, members, failure destination), forwarding + DND controls on the Extensions page, feature-code list on the System page. Phase 4 placeholder pages become real; Dial Plans stays the Phase 2 Routes page.

## Out of scope

- Voicemail transcription (speech-to-text).
- Intercom/paging, call pickup (**deferred** — pickup needs directed-pickup codes and group-pickup wiring; tracked as a follow-up story), dictation, blind transfer to an IVR menu.
- Voicemail-to-email with custom templates; a fixed sensible template only.
  -.per-user ring-group membership weighting UI beyond a number field.
- WebRTC (spec §3 stays SIP-only).
- Conference rooms.

## Constraints

- No PostgreSQL call on the SIP path (as before); voicemail audio I/O happens in the call's media goroutine with the Phase 3 state-timeout discipline, MinIO client calls bounded by `HELLO_SIP_STATE_TIMEOUT`.
- Voicemail recording and greeting playback need in-band audio: the B2BUA anchors media **only** for voicemail calls (the first anchored media use, per spec §5's "optional anchored media"); all other calls stay direct. The anchor is a small in-node RTP recorder/player behind a Hello-owned interface (`internal/media`), not a separate service.
- Secrets (voicemail passwords, SMTP credentials, MinIO keys) follow the Phase 3 rules: encrypted at rest via the sops files in `deploy/kuvryn-sync/kw/`, injected as env from `internal/config`, and never in logs or API responses (enforced by `TestNoSecretsInLogs` and the redaction tests).
- Email delivery never blocks the SIP path: a queue worker like the Phase 1 CDR writer.
- New dependencies: MinIO Go SDK v7 (`github.com/minio/minio-go/v7`); SMTP via stdlib `net/smtp`.
- Deterministic group ordering: sequential/round-robin by position; weighted via the Phase 2 seeded-shuffle approach.
- Every task: gofmt/vet/golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, mutation checks on guarantees; REVIEW.md applies. CI runs on the Arc runners (scout shape); deployments pin Nexus digests via kuvryn-sync.

### Shared contracts (fixed; a stream that must change one asks the lead)

1. **Migration `00004_pbx_features.sql`** (written first, by the lead): `voicemail_boxes` (id, extension_id unique, password_hash, email, greeting_object, unreachable_object, timestamps), `voicemail_messages` (id, box_id, minio_object, caller, duration_ms, heard, email_status, timestamps), `ring_groups` (id, name, strategy, hunt, ring_timeout, member_delay, ignore_dnd, failure_kind, failure_target, timestamps), `ring_group_members` (group_id, extension_id, position, weight, delay), `extensions` gains `dnd bool`, `forward_always`, `forward_busy`, `forward_no_answer` (text, empty = off), `voicemail_enabled bool default true`; `feature_codes` (code unique, action, argument).
2. **Livestate presence:** `internal/livestate` gains `SetDeviceState(ctx, device, state, ttl)` / `DeviceStates(ctx) ([]DeviceState, error)` with `DeviceState{Device, Extension, State, UpdatedAt}` in a `hello:presence:` keyspace; hello-sip publishes state changes (ringing/on-call from the B2BUA, DND from the registrar state) and hello-control serves them.
3. **MinIO:** bucket `hello-voicemail`; object key `box/<box-id>/<timestamp>-<callid>.wav`; the S3 client is built from `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_SECURE` (config, sops for the keys); presigned GET URLs (15 min) are produced by hello-control for the UI.
4. **Feature-code actions** (the action column of the feature_codes table (migration 00004)): `forward_always`, `forward_busy`, `forward_no_answer`, `dnd_on`, `dnd_off`, `voicemail`, `blind_transfer`, `attended_transfer`.
5. **HTTP JSON:** `PATCH /api/v1/extensions/{id}` gains `dnd`, `forwardAlways`, `forwardBusy`, `forwardNoAnswer`, `voicemailEnabled`; `GET|PUT /api/v1/extensions/{id}/voicemail` (box settings: password change, email, greetings upload via multipart), `GET /api/v1/voicemail/messages?box=<id>&unheard=`, `POST /api/v1/voicemail/messages/{id}/heard`, `DELETE .../messages/{id}`, `GET /api/v1/voicemail/messages/{id}/audio` (302 to a presigned URL); `GET|POST /api/v1/ring-groups`, `GET|PATCH|DELETE /api/v1/ring-groups/{id}`; `GET|PUT /api/v1/feature-codes`; presence via `GET /api/v1/presence`. All in the OpenAPI document; the UI consumes only these.
6. **B2BUA integration points:** forwarding/DND evaluated in `internal/sip` before forking (using the snapshot, which gains the new extension fields); group resolution replaces the direct fork when the dialled number is a group number (groups live in the snapshot too); hold/transfer stay in-dialog and never touch the database.

## Task 1: Contracts (lead)

Files: migration 00004, livestate presence, config additions (`MINIO_*`, SMTP, group defaults), this spec/plan ticks, `.github` untouched.

- [ ] Write and verify the migration (`HELLO_TEST_DATABASE_URL` test), livestate presence with tests, config fields with tests. Commit to `phase-4-features`; branch `phase-4-sip`, `phase-4-control`, `phase-4-ui` from it.

## Task 2: SIP features (branch phase-4-sip)

Files: `internal/sip/` (hold, transfers, forwarding/DND/group resolution, feature codes, voicemail call flow, SUBSCRIBE/NOTIFY), `internal/media/` (anchored recorder/player), `internal/livestate/` presence publish, `internal/snapshot/` (new fields), `internal/config/` (new settings only with tests).
Interfaces: consumes contracts 1–6; produces the DTMF/REFER behaviour, MWI MESSAGEs, presence publishes.

- [ ] Hold (S-1) with tests incl. a mutation check.
- [ ] Blind transfer (S-2) with tests: extension target, external target (through routing), failure NOTIFY.
- [ ] Attended transfer (S-3) with tests incl. CDR linkage.
- [ ] Forwarding + DND (S-4, S-5) with tests: each kind, loop detection, voicemail interplay.
- [ ] Groups (S-6, S-7): strategy unit tests (a table per strategy), then B2BUA integration with in-process phones.
- [ ] Voicemail call flow (S-8): anchored media, greeting, beep, record, `#`/`*` keys, MinIO store (MinIO in-process container test), MWI MESSAGE.
- [ ] Presence (S-10): SUBSCRIBE/NOTIFY per contract 2, digest-auth'd, expiry.
- [ ] Feature codes (S-11): dispatch tests per action.
- [ ] Gate + report per house rules.

## Task 3: Control plane (branch phase-4-control)

Files: `internal/store/` (new tables, voicemail queries incl. MinIO object keys), `internal/api/` (routes per contract 5, presigned URLs, multipart greeting upload), `internal/mailer/` (SMTP queue worker), `cmd/hello-control/main.go` (MinIO client, worker wiring).
Interfaces: produces contract 5; consumes contract 3.

- [ ] Store + handlers per contract 5 with tests (`TestVoicemailBoxSettings`, `TestVoicemailMessageFlow` with MinIO, `TestRingGroupCRUD`, `TestFeatureCodes`, `TestPresenceAPI`).
- [ ] Email worker (S-8): retry/backoff, `email_status` recorded, `TestMailerRetries` with a fake SMTP server (in-process).
- [ ] OpenAPI for every route; `TestVersionAndOpenAPI` stays green.
- [ ] Gate + report.

## Task 4: UI (branch phase-4-ui)

Files: `web/src/` (Voicemail, RingGroups pages, Extensions additions, System feature codes, tests).

- [ ] Pages per S-14 with tests per the criteria; mutation checks on the voicemail playback gate and the group strategy editor.
- [ ] `pnpm` gate + report.

## Task 5: Deploy + integration (lead)

Files: `deploy/kuvryn-sync/kw/resources.yaml` (+ MinIO, SMTP secret skeleton), compose equivalents, `test/integration/` feature tests, README/`docs/features.md`.

- [ ] MinIO in kw + compose; integration tests for hold/transfer/forward/DND/groups/voicemail through the lab or the cluster; metrics test; gate; then pin digests and let Sync roll it out to kw, verify on the deployed instance.

## Interfaces

- **Env:** `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_SECURE` (both services; hello-control also needs presigning), `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM` (hello-control).
- **HTTP:** the routes in contract 5, all in the OpenAPI document.
- **SIP:** REFER (with blind fallback `*2` DTMF), hold re-INVITEs, SUBSCRIBE/NOTIFY (event `dialog`, BLF), MWI MESSAGEs, feature-code DTMF handling in-dialog.
- **UI routes:** `/voicemail`, `/ring-groups`, extensions page additions, `/system` feature codes.

## Data

- **PostgreSQL (migration 00004):** the tables listed in In scope (S-1..S-11 bullets) — `voicemail_boxes`, `voicemail_messages`, `ring_groups`, `ring_group_members`, `feature_codes`, and new extension columns (`dnd`, the three forwarding targets, `voicemail_enabled`).
- **MinIO:** bucket `hello-voicemail`, object key `box/<box-id>/<timestamp>-<callid>.wav`.
- **Valkey:** presence keyspace per contract 2.
- **Git:** images pinned in `deploy/kuvryn-sync/kw/resources.yaml` as today.

## Edge cases

- Transfer target is the transferring party, or an external number that fails — the NOTIFY reports failure and both legs stay up.
- Attended transfer where the second call was never answered — treated as blind transfer to the second target.
- A forwarding chain A→B→C→A — detected, 408 "forward loop" to the caller.
- DND extension in a ring-all group with `ignore_dnd: false` — the member is skipped and the group still completes if others answer.
- Round-robin position wraps; weighted groups with a member weight 0 (never first, still rings last).
- Voicemail while the caller hangs up during the beep — recording stops at hangup; a message shorter than 1s is discarded.
- Two phones SUBSCRIBE for the same extension; one expires mid-call — the other still gets NOTIFYs.
- MinIO down: voicemail calls still complete; the message is recorded to MinIO with retry, and on continued failure the CDR notes voicemail failure and MWI reports zero messages.
- SMTP down: email_status cycles to failed after 3 retries; the message itself is unaffected.

## Failure modes

- **MinIO unavailable:** voicemail calls still complete; audio upload retries 3x with backoff; on failure the message row is marked failed, MWI reports zero and the UI shows the failure; nothing blocks the SIP path (bounded by `HELLO_SIP_STATE_TIMEOUT`).
- **SMTP unavailable:** the email worker retries 3x with backoff, then records `email_status=failed` on the message; the voicemail itself is unaffected.
- **`internal/media` cannot bind RTP:** the call goes to voicemail without recording (CDR notes it) rather than dropping the call.
- **Presence flood:** a state change that touches N subscribers fans out from the node's goroutine pool, not the SIP transaction path.

## Acceptance criteria

- [ ] [S-1] `TestHold` (in-process phones) passes — fails if the held leg's SDP is not sendonly, the resumed leg's is not sendrecv, or the CDR changes.
- [ ] [S-2] `TestBlindTransfer` passes — fails if the transferee is not connected to the target after REFER+NOTIFY, or an external target does not traverse outbound routing; `TestBlindTransferNotifyFailure` fails if a failed transfer leaves the legs up.
- [ ] [S-3] `TestAttendedTransfer` passes — fails if the two original parties are not connected after the bridge or the transferee's legs do not drop; CDR linkage asserted.
- [ ] [S-4] `TestForwarding` passes — fails if always/busy/no-answer each do not forward when they should (and do not when they should not), or a forwarding chain that loops does not reject with 408.
- [ ] [S-5] `TestDND` passes — fails if a DND extension rings instead of going to voicemail/rejecting.
- [ ] [S-6] `TestRingGroupStrategies` (unit, per-strategy tables incl. DND handling) and `TestRingGroupsIntegration` pass — fail if any strategy orders or times out members other than per its definition, or the failure destination does not apply.
- [ ] [S-7] `TestHuntGroup` passes — fails if two members ring simultaneously, or the hunt does not advance on no-answer/busy.
- [ ] [S-8] `TestVoicemailLeaveMessage` passes — fails if no WAV lands in MinIO with correct metadata, the greeting/beep order is wrong, `#`/`*` are ignored, MWI counts do not update, or the email (with attachment) is not sent/recorded; `TestVoicemailBusyAndDND` covers busy/DND immediate routing.
- [ ] [S-9] `TestVoicemailRetrieval` passes — fails if `*97` without a correct password does not prompt, messages are not listed/played/heard/deleted end to end (API + MinIO object removal).
- [ ] [S-10] `TestPresenceBLF` passes — fails if a SUBSCRIBING phone does not receive NOTIFY on idle→ringing→on-call→idle transitions, or the subscription does not expire.
- [ ] [S-11] `TestFeatureCodes` passes — fails if any default code does not perform its action or an unknown code is not rejected.
- [ ] [S-13] `TestFeatureMetrics` passes — fails if voicemail, transfer, forwarding, group and hold metrics do not move through their flows.
- [ ] [S-14] `procoder test`/`lint` pass over `web/`; `Voicemail.test.tsx` fails if a message cannot be played or deleted, `RingGroups.test.tsx` fails if a strategy is not persisted, `Extensions` DND/forwarding controls fail their tests.
- [ ] [S-12] [S-8] `TestLabSmoke` (or the cluster equivalent) leaves a voicemail through the deployed instance and it appears in MinIO — fails otherwise.

## Open questions

<!-- All resolved 2026-10-04; answers in .procoder/ask/answers.md and folded into Source and In scope. -->
