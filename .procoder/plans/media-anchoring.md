# media-anchoring — implementation plan

Status: draft
Spec: .procoder/specs/media-anchoring.md

## Goal

Calls anchor RTP through Hello only when a feature needs it — NAT, recording, announcements, voicemail — gaining NAT-safe audio, on-demand recording, announcements, and RTP metrics, with everything else staying direct.

## Architecture

`internal/media` grows from the Phase 4 recorder/player into a bidirectional relay: per-session goroutines with bounded queues, symmetric-RTP latching, sequence/timestamp rewrite. The B2BUA decides anchoring at setup (and on re-INVITE) from four triggers: NAT detection, recording active, announcement/voicemail feature, or the force switch. Recording and announcements use the two new MinIO buckets; metadata is Postgres (migration 00005); the UI gets Recordings and Announcements pages.

## Constraints

- Anchor sessions live on the owning hello-sip node; RTP ports from `HELLO_RTP_PORT_MIN/MAX` (default 20000–21000); bind failure falls back to direct with a trace step and metric.
- Per-session goroutines, bounded queues (drop-oldest, counted), ~5ms added latency budget; panic recovery per session with direct re-INVITE fallback.
- No transcoding: PCMU/PCMA/G.722/Opus pass through; recordings store the received payload (codec noted).
- Secrets via sops; MinIO I/O off the SIP transaction path (media goroutine, bounded by the Phase 3 discipline).
- Gate per task: gofmt/vet/golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, mutation checks; REVIEW.md applies. CI on the Arc runners; no workloads on the user's Mac.

### Shared contracts (fixed; a stream that must change one asks the lead)

1. **Migration `00005_media.sql`** (Task 1):
   ```sql
   CREATE TABLE recordings (
     id BIGSERIAL PRIMARY KEY,
     correlation_id TEXT NOT NULL UNIQUE,
     minio_object TEXT NOT NULL,
     initiated_by TEXT NOT NULL CHECK (initiated_by IN ('dtmf','default','api')),
     duration_ms BIGINT NOT NULL DEFAULT 0,
     created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   CREATE TABLE announcements (
     id BIGSERIAL PRIMARY KEY,
     name TEXT NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
     minio_object TEXT NOT NULL,
     created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
     updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   ALTER TABLE extensions ADD COLUMN record_default BOOLEAN NOT NULL DEFAULT FALSE;
   ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
   ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
     CHECK (failure_kind IN ('none','voicemail','external','announcement'));
   ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
   ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
     CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                       'voicemail','blind_transfer','attended_transfer','announcement'));
   ```
2. **Anchoring decision (internal/sip, exported for tests):** `type AnchorReason string` with values `""` (direct), `"nat"`, `"recording"`, `"announcement"`, `"voicemail"`, `"forced"`; decided by `func decideAnchor(req *sip.Request, snap *snapshot.Snapshot, from, to EndpointInfo, force bool) AnchorReason`. NAT detection: contact host:port ≠ packet source, or known NAT flag.
3. **Relay (internal/media):** `type Relay struct` built per anchored session: `NewRelay(minPort, maxPort int) (*Relay, error)` allocates the RTP pair from the range; `Relay.AddLeg(name string) (port int, err error)`; audio flows leg-to-leg with seq/ts rewrite and SSRC management; symmetric latching (reply to first source, refresh on change); `Metrics() RelayStats` (packets/octets/loss/jitter per direction); session goroutines with bounded queues (drop-oldest) and panic recovery.
4. **MinIO buckets:** `hello-recordings` (key `rec/<unix>-<callid>.wav`) and `hello-announcements` (key `ann/<name>.wav`) — created at hello-control startup (bucket-init exists since Phase 4).
5. **Recording state:** per-call recording state in the B2BUA call struct; `*1` and the snapshot's `RecordDefault` trigger it; the notice announcement (named `recording-notice` in the announcement set) plays first when present; on end, the WAV goes to MinIO with retry (3×, bounded in-memory hold ≤10MB) and a `recordings` row links the correlation id.
6. **HTTP JSON (camelCase, same error envelope):** `GET /api/v1/recordings?extension=&before=&limit=` → `{"items":[{id,correlationId,initiatedBy,durationMs,createdAt}],"next"}`; `GET /api/v1/recordings/{id}/audio` → 302 presigned (15 min); `DELETE /api/v1/recordings/{id}` → 204; `GET /api/v1/announcements` → items `{id,name,createdAt}`; `POST /api/v1/announcements` multipart (name + WAV file, ≤10MB, content-type audio/*) → 201; `DELETE /api/v1/announcements/{id}` → 204; `PATCH /api/v1/extensions/{id}` gains `recordDefault`.
7. **Feature codes/destinations:** `feature_codes.action` and `ring_groups.failure_kind` gain `announcement` (argument = announcement name); a pre-transfer announcement is the feature-code argument on `attended_transfer`/`blind_transfer` when set (plays before the transfer executes).

## Task 1: Contracts (lead)

Files: `migrations/00005_media.sql`, buckets created at startup (hello-control bucket-init gains recordings/announcements), `.github` untouched.

- [x] Migration verified with `HELLO_TEST_DATABASE_URL=… go test -run Migrate ./test/integration/` → ok.

## Task 2: Media + SIP (branch phase-5-media)

Files: `internal/media/` (relay per contract 3), `internal/sip/` (anchoring decision, recording state + `*1`, announcements on destinations/pre-transfer, presence-adjacent metrics), `internal/snapshot/` (`RecordDefault`, announcements list, widened enums), `internal/config/` (RTP range + force switch, with tests).

- [ ] Relay: `TestRelayBidirectional`, `TestRelayLatching`, `TestRelayMetrics` (loopback RTP), `TestRelayPortExhaustion`; mutation checks on latching and rewrite.
- [ ] Anchoring decision: `TestConditionalAnchor` (direct stays direct; each trigger anchors; force switch), `TestReAnchorMidCall`; mutation check.
- [ ] Recording: `TestRecording` (`*1`, record_default, notice, MinIO WAV, CDR link, transfer continuation), `TestRecordingPauseOnHold`; mutation check.
- [ ] Announcements: `TestAnnouncements` (destination kind, pre-transfer, feature code, missing-WAV skip).
- [ ] NAT carrier case: `TestNATAnchorCarrier` (NAT-simulated trunk, direct fails, anchored works).
- [ ] Metrics: `TestRTPMetrics`, anchor failure counters. Gate + report per house rules.

## Task 3: Control plane + UI (branch phase-5-control)

Files: `internal/store/` (recordings/announcements queries), `internal/api/` (contract 6 routes, multipart upload, presigned audio), `internal/config/` untouched, `cmd/hello-control` (bucket init), web/ pages (`Recordings.tsx`, `Announcements.tsx`, Extensions record-default control), OpenAPI.

- [ ] Store + handlers per contract 6 with tests (`TestRecordingsAPI` with MinIO, `TestAnnouncementsAPI`, validation tests); audit/revision on mutations.
- [ ] UI pages + tests (`Recordings.test.tsx`, `Announcements.test.tsx`, Extensions control), mutation checks.
- [ ] OpenAPI; `TestVersionAndOpenAPI` green. Gate + report.

## Task 5: Deploy + integration (lead)

Files: kw resources.yaml (RTP port range env, announcements/recordings buckets), compose equivalents, integration tests, `docs/media.md` completion, digest pinning + Sync rollout, live verification.

- [ ] Integration tests (hold/record/announce through the lab or cluster); metrics test on the deployed instance; pin digests; Sync rollout; verify on kw; report how to use recording and announcements.
