# pbx-features — implementation plan

Status: draft
Spec: .procoder/specs/pbx-features.md

## Goal

The day-to-day PBX features on top of the Phase 1–3 call engine: hold, blind and attended transfer, forwarding, DND, ring/hunt groups (five strategies), voicemail (MinIO + MWI + email), presence/BLF, and DTMF feature codes — each behind named tests, deployed to kw through the existing pipeline.

## Architecture

The B2BUA (internal/sip) evaluates per-extension state (DND, forwarding) and group/voicemail routing from the revisioned snapshot before forking; hold and transfers stay in-dialog. Voicemail audio is the first anchored-media use: a small `internal/media` recorder/player joins the B2BUA leg for voicemail calls only. Presence and voicemail metadata flow through the existing Valkey/Postgres split; email leaves via a queue worker like the CDR writer.

## Constraints

- No PostgreSQL/MinIO call on the SIP transaction path; media-goroutine I/O bounded by `HELLO_SIP_STATE_TIMEOUT`.
- Secrets via sops files in `deploy/kuvryn-sync/kw/`, injected by `internal/config`, never in logs or API responses.
- New dependencies: only `github.com/minio/minio-go/v7`. SMTP stays stdlib.
- Deterministic group ordering; weighted strategy uses the Phase 2 seeded-shuffle approach.
- Gate per task: gofmt, go vet, golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, mutation checks on every guarantee; REVIEW.md applies.
- CI on the Arc runners (scout shape, as merged); no workloads on the user's Mac.

### Shared contracts (fixed; a stream that must change one asks the lead)

1. **Migration `00004_pbx_features.sql`** (Task 1, exact DDL by the lead before streams branch): tables `voicemail_boxes(id, extension_id UNIQUE, password_hash, email, greeting_object, unreachable_object, created_at, updated_at)`, `voicemail_messages(id, box_id REFERENCES voicemail_boxes ON DELETE CASCADE, minio_object, caller, duration_ms, heard bool, email_status text DEFAULT 'pending', created_at)`, `ring_groups(id, name UNIQUE, strategy CHECK IN ('ring-all','sequential','round-robin','longest-idle','weighted'), hunt bool, ring_timeout int, member_delay int, ignore_dnd bool, failure_kind CHECK IN ('none','voicemail','external'), failure_target text, timestamps)`, `ring_group_members(group_id, extension_id, position, weight, delay, PRIMARY KEY(group_id, extension_id))`, plus `ALTER TABLE extensions ADD dnd bool DEFAULT false, ADD forward_always/forward_busy/forward_no_answer text DEFAULT '', ADD voicemail_enabled bool DEFAULT true` and `feature_codes(code TEXT PRIMARY KEY, action TEXT, argument TEXT DEFAULT '')`.
2. **Snapshot additions (internal/snapshot):** `Extension` gains `DND bool`, `ForwardAlways/Busy/NoAnswer string`, `VoicemailEnabled bool`, `VoicemailBoxID int64`; the query LEFT JOINs voicemail_boxes. `Snapshot.RingGroup(number) (RingGroup, []RingGroupMember, bool)` with members ordered by position. `RingGroup{ID, Name, Strategy, Hunt, RingTimeout, MemberDelay, IgnoreDND, FailureKind, FailureTarget}`.
3. **Livestate presence (internal/livestate):** `SetDeviceState(ctx, DeviceState, ttl)` / `DeviceStates(ctx)` with `DeviceState{Device, Extension, State, UpdatedAt}` in `hello:presence:{device}` (State: `idle|ringing|on-call|dnd`); hello-sip publishes, hello-control reads.
4. **Config (internal/config):** `Control` gains `MinioEndpoint, MinioAccessKey, MinioSecretKey, MinioSecure bool, SmtpHost string, SmtpPort int, SmtpUser, SmtpPass, SmtpFrom string` (SMTP all optional; if host is empty the mailer is disabled); `SIP` gains the same Minio* fields. Validation: MinioEndpoint required with keys, SmtpPort 1–65535, never echo secrets.
5. **Media interface (internal/media, Task 2):** `type Session interface { Play(ctx, audio []byte) error; Record(ctx, maxDur time.Duration, dtmf chan<- byte) ([]byte, error); Close() error }` built from the B2BUA leg's negotiated audio; WAV encode/decode PCM. Anchor only for voicemail calls.
6. **MinIO (contract shared by Tasks 2/3/5):** bucket `hello-voicemail`; object key `box/<box-id>/<unix>-<callid>.wav`; presigned GET 15 min; client built once at startup from config.
7. **HTTP JSON (camelCase, same error envelope):** `PATCH /api/v1/extensions/{id}` gains `dnd, forwardAlways, forwardBusy, forwardNoAnswer, voicemailEnabled`; `GET/PUT /api/v1/extensions/{id}/voicemail` (PUT: password, email; multipart `greeting`/`unreachable` files); `GET /api/v1/voicemail/messages?box=&unheard=`, `POST /api/v1/voicemail/messages/{id}/heard`, `DELETE /api/v1/voicemail/messages/{id}`, `GET /api/v1/voicemail/messages/{id}/audio` → 302 presigned; `GET|POST /api/v1/ring-groups`, `GET|PATCH|DELETE /api/v1/ring-groups/{id}`; `GET|PUT /api/v1/feature-codes`; `GET /api/v1/presence`. Validation: strategy enum, failure kinds, positions ≥1, weights ≥0, codes match `^\*[0-9#]{2,4}$` (plus `##`).

## Task 1: Contracts (lead)

Files: `migrations/00004_pbx_features.sql` (contract 1 DDL), livestate presence + tests (contract 3), config fields + tests (contract 4), `.github` untouched.

- [x] Write the migration; verify with `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/` → ok.
- [x] Livestate presence with `TestPresenceKeyspace` (real Valkey: set/list/expiry) → pass.
- [x] Config fields with `TestLoadMinioSmtp` → pass.
- [x] Commit to `phase-4-features`; branch `phase-4-sip`, `phase-4-control`, `phase-4-ui` from it.

## Task 2: SIP features (branch phase-4-sip)

Files: `internal/sip/` (hold, transfers, forwarding/DND/group/voicemail call flow, feature codes, SUBSCRIBE/NOTIFY, MWI), `internal/media/`, `internal/livestate/` presence publish hooks, `internal/snapshot/` per contract 2.
Interfaces: contracts 2–6; produces MWI MESSAGEs, presence publishes, the anchored-media Session.

- [x] Snapshot additions per contract 2 (+ tests: DND/forwarding/group/box fields load; LEFT JOIN leaves box-less extensions voicemail-disabled).
- [x] Hold (S-1): sendonly/sendrecv relay per direction; tests incl. mutation check.
- [x] Blind transfer (S-2): REFER → NOTIFY (100/200/failure), new originations; tests for extension/external/failed targets.
- [x] Attended transfer (S-3): bridging with CDR linkage; tests.
- [x] Forwarding + DND (S-4, S-5): evaluation order (always → DND/voicemail → busy → no-answer), loop detection (408), tests per kind + interplay with voicemail.
- [x] Groups (S-6/S-7): strategy resolution as pure unit-tested functions (per-strategy tables), then fork integration; failure destinations.
- [x] Voicemail flow (S-8): anchor Session, greeting → beep → record (DTMF `#` end, `*` retry), MinIO put with retry, MWI MESSAGE (counts from the store), busy/DND immediate routing.
- [x] Presence (S-10): publish on B2BUA state changes; SUBSCRIBE/NOTIFY dialog package with digest auth and expiry.
- [x] Feature codes (S-11): in-dialog DTMF dispatch per contract 1 actions; tests per action.
- [x] Metrics per spec S-13. Gate + report per house rules (mutation checks on hold, transfer, forwarding loop, group strategies, voicemail store).

## Task 3: Control plane (branch phase-4-control)

Files: `internal/store/` (new tables; voicemail queries; group queries; feature-code queries), `internal/api/` (contract 7 routes, presigned URLs, multipart greeting upload), `internal/mailer/` (SMTP queue worker: pick up `email_status='pending'` rows, send with WAV attachment, retry 3× backoff, record `sent`/`failed`), `cmd/hello-control/main.go` (MinIO client + mailer wiring).
Interfaces: produces contract 7; consumes contracts 1, 3, 6.

- [ ] Store: box settings, message list/mark/delete (delete removes the MinIO object too — via callback to avoid a store→MinIO dependency), group CRUD with member positions, feature-code upsert; audit + revision bump + NOTIFY on every mutation.
- [ ] Handlers per contract 7 + presigned GETs; OpenAPI for all; `TestVersionAndOpenAPI` stays green.
- [ ] Mailer with fake-SMTP tests (`TestMailerRetries`, `TestMailerDisabled`).
- [ ] API tests: `TestVoicemailBoxSettings`, `TestVoicemailMessagesFlow` (with MinIO container), `TestRingGroupCRUDValidation`, `TestFeatureCodeRoutes`, `TestPresenceAPI`.
- [ ] Gate + report.

## Task 4: UI (branch phase-4-ui)

Files: `web/src/` — `pages/Voicemail.tsx` (list, filter unheard, play via the audio endpoint, mark heard/delete, box settings dialog incl. greeting upload), `pages/RingGroups.tsx` (CRUD, strategy select, member editor with position/weight/delay, failure destination), Extensions page DND/forwarding/voicemail controls, `pages/System.tsx` feature-code editor; api.ts + tests.

- [ ] Pages per S-14 with tests per the spec criteria; mutation checks on playback gating and strategy persistence.
- [ ] `pnpm typecheck/lint/test/build` + report.

## Task 5: Deploy + integration (lead)

Files: `deploy/kuvryn-sync/kw/resources.yaml` (MinIO deployment/Service/bucket init, SMTP sops secret skeleton, hello feature env), compose equivalents, `test/integration/` feature tests, README, `docs/features.md`.

- [ ] kw + compose MinIO; SMTP secret skeleton (sops).
- [ ] Integration tests: hold, transfers, forwarding kinds, DND, group strategies, voicemail leave/retrieve, presence — through the lab/cluster.
- [ ] Metrics test; `docs/features.md`; pin digests; Sync rollout to kw; verify on the deployed instance; report how to use each feature.

## Acceptance criteria

See `.procoder/specs/pbx-features.md` — each criterion cites its named test.
