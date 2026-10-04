# media-anchoring

Status: complete

Source: `hello-pbx-spec.md` §28 Phase 5 (Media), §16 (codecs and media), §5 (separate signaling and media). Decided 2026-10-04 (`.procoder/ask/answers.md`): **conditional anchoring** (calls stay direct RTP unless a feature needs the anchor); **on-demand recording** (DTMF `*1` and per-extension API default, MinIO storage, spoken notice); **announcements in scope** (named uploaded sets, playable as failure destinations, before transfer, or via feature code). Voicemail's anchored media already ships (Phase 4).

## Problem

Every Hello call so far is direct RTP between endpoints — right for most calls, but it leaves three gaps: phones behind symmetric NAT get one-way or no audio with some carriers, there is no way to record a call, and there is no way to play a spoken announcement. Phase 5 turns the Phase 4 media anchor into a full conditional relay: a call anchors only when a feature needs it, gaining NAT-safe audio, recording, announcements and real RTP metrics on exactly the calls that require them.

## Users

- **Phone users:** NAT'd phones get working audio against carriers; `*1` toggles recording mid-call; they hear an announcement before a transfer or on a failure destination.
- **Administrators:** upload announcement WAVs, set per-extension default recording, download recordings in the UI.
- **Operators:** RTP packet/loss/jitter metrics for anchored calls; recording storage usage.

## In scope

- [S-1] **Conditional anchoring in the B2BUA:** a call anchors when (a) either endpoint is detected NAT'd (contact host:port differs from the source, or the Phase 3 NAT flags), (b) recording is active, (c) an announcement or voicemail feature is playing, or (d) the config forces it. Otherwise the call stays direct. A mid-call re-INVITE that newly requires anchoring re-anchors live; an anchored call whose need disappears stays anchored to its end (no churn).
- [S-2] **RTP relay in `internal/media`:** the anchor becomes a bidirectional relay — two legs, sequence/timestamp rewrite, SSRC management, symmetric RTP learn (reply to the first packet's source per leg), Latching refresh. Direct calls never touch it.
- [S-3] **RTP metrics:** `hello_rtp_sessions` (gauge), `hello_rtp_packets_total{direction}`, `hello_rtp_octets_total{direction}`, `hello_rtp_loss_total{direction}`, `hello_rtp_jitter_ms{direction}` (histogram), `hello_media_anchored_calls` (gauge).
- [S-4] **Call recording:** per-extension `record_default` (API), plus `*1` DTMF toggle mid-call. A spoken notice from the announcement set plays to both parties before recording starts (config-gated). Recordings are WAV in MinIO bucket `hello-recordings`, key `rec/<unix>-<callid>.wav`; the CDR links the recording object; the UI lists/plays/downloads them. Multi-leg calls after a transfer record the bridged audio.
- [S-5] **Announcements:** named sets of uploaded WAVs (API multipart, stored in MinIO bucket `hello-announcements`); playable as a ring-group/failure destination kind (`announcement`), optionally before a blind transfer (per outbound route or feature-code argument), and via a feature code. An announcement destination answers, plays, then continues (hangup or next step).
- [S-6] **NAT handling via the anchor:** when a call anchors, both legs send RTP to Hello, so NAT pinholes are kept open by real traffic; the anchor refreshes latching on source changes. Direct calls keep the Phase 1–4 `rport`/symmetric behaviour. The Phase 2 trunk calls anchor when either side is NAT-detected (carrier fixes one-way audio against NAT'd phones).
- [S-7] **Metrics/ops:** recording storage usage (`hello_recording_storage_bytes`), anchor session count, per-anchor packet counters; the anchoring matrix and codec pass-through documented in `docs/media.md` (PCMU/PCMA/G.722/Opus pass through; no transcoding).

## Out of scope

- Transcoding of any kind (spec §3); SRTP; conferencing; music-on-hold (hold stays sendonly pass-through); text/caption extraction from recordings; per-call dual-channel recording separation; external RTP engines (rtpengine/FreeSWITCH) — `internal/media` stays behind the Hello-owned interface per spec §16.

## Constraints

- The anchor runs on the hello-sip node that owns the call (no cross-node media); ports drawn from a configured RTP range (`HELLO_RTP_PORT_MIN/MAX`, default 20000–21000), opened only while a session lives.
- Anchoring must not exceed ~5ms added latency per hop on the lab; packet processing on a per-session goroutine with bounded queues (drop-oldest on overflow, counted in metrics).
- Codecs pass through untouched — no decoding except G.711 for voicemail/announcement mixing (already in `internal/media`).
- Recordings/announcements: MinIO buckets `hello-recordings`, `hello-announcements`; access via presigned URLs like voicemail; secrets via sops.
- DTMF detection for `*1` uses the existing in-band G.711 detector plus RFC 2833 telephone-event passthrough detection.
- Gate per task: gofmt/vet/golangci-lint (0), `go test -race ./...` green, `procoder check` 0 blocking, mutation checks; REVIEW.md applies. CI on the Arc runners; deployment via Sync; no workloads on the user's Mac.

## Interfaces

- **Env:** `HELLO_RTP_PORT_MIN`/`HELLO_RTP_PORT_MAX` (hello-sip), `HELLO_MEDIA_FORCE_ANCHOR` (bool, default false — the config "force" switch in S-1d).
- **HTTP:** `GET /api/v1/recordings?extension=&before=&limit=` (paged like CDRs), `GET /api/v1/recordings/{id}/audio` (302 presigned), `DELETE /api/v1/recordings/{id}`; `GET|POST /api/v1/announcements`, `DELETE /api/v1/announcements/{id}` (multipart WAV upload; name unique); `PATCH /api/v1/extensions/{id}` gains `recordDefault`. All in the OpenAPI document.
- **Data (migration `00005_media.sql`):** `recordings(id, correlation_id UNIQUE, minio_object, initiated_by CHECK IN ('dtmf','default','api'), duration_ms, created_at)`, `announcements(id, name UNIQUE, minio_object, created_at, updated_at)`, `extensions` gains `record_default bool DEFAULT false`; the `ring_groups` `failure_kind` CHECK (migration 00004) and the `feature_codes` `action` CHECK widened to include `announcement`.
- **CDR:** `recordings` linked by correlation id (no CDR column change); the routing trace gains anchor steps ("media anchored (nat)", "recording started").

## Data

- **PostgreSQL (migration `00005_media.sql`):** `recordings(id, correlation_id UNIQUE, minio_object, initiated_by CHECK IN ('dtmf','default','api'), duration_ms, created_at)`, `announcements(id, name UNIQUE, minio_object, created_at, updated_at)`, `extensions` gains `record_default bool DEFAULT false`; the `ring_groups` `failure_kind` CHECK (migration 00004) and the `feature_codes` `action` CHECK widened to include `announcement`.
- **MinIO:** buckets `hello-recordings` (key `rec/<unix>-<callid>.wav`) and `hello-announcements` (key `ann/<name>.wav`).
- **CDR:** recordings linked by correlation id; the routing trace gains anchor steps.

## Edge cases

- Both parties NAT'd and recording on: one anchor session, two latched legs.
- Re-INVITE hold during recording: recording pauses (no audio flows), resumes after.
- Transfer of a recorded call: the recording follows the bridge (same correlation), the CDR link stays.
- Anchor port exhaustion: the call falls back to direct, the trace records it, `hello_media_anchor_failures_total` counts it.
- Announcement destination + voicemail failure in the same group: announcement plays first, then voicemail.
- Codec mismatch after re-INVITE (PCMA → Opus) on an anchored call: pass-through continues; if a recording needs G.711 and the codec is Opus, the recording stores the pass-through payload with the codec noted (no transcode).
- RFC 2833 and in-band DTMF at once: 2833 wins; deduplicated.
- Direct call (never anchored) with `record_default` on: the call anchors at setup (need (b) applies), so recording always works.

## Failure modes

- **Anchor RTP bind failure** (port range exhausted, host issue): fall back to direct with a trace step and metric; recording/announcement for that call fails with a CDR note, the call itself completes.
- **MinIO unavailable at recording end:** the recording retries 3× with backoff from a bounded in-memory hold (≤10 MB per recording, else discarded with a CDR note and metric), mirroring the voicemail pattern.
- **Announcement WAV missing from MinIO:** the destination step is skipped with a trace step and metric; the call continues to its next step.
- **Relay goroutine panic:** recovered per session; the session falls back to direct re-INVITE if the call is still up, else ends normally; counted in `hello_media_anchor_failures_total`.

## Acceptance criteria

- [ ] [S-1] `TestConditionalAnchor` passes — fails if a clean direct call anchors, or a NAT-simulated/recording/announcement call does not anchor; `TestReAnchorMidCall` fails if a mid-call anchor need does not re-anchor live.
- [ ] [S-2] `TestRelayBidirectional` passes — fails if audio does not flow both ways through the relay, or sequence/timestamp/SSRC handling breaks on leg swap.
- [ ] [S-3] `TestRTPMetrics` passes — fails if packets/octets/loss/jitter do not move for an anchored call.
- [ ] [S-4] `TestRecording` passes — fails if `*1` or `record_default` does not produce a MinIO WAV linked to the CDR, if the notice does not play when configured, or a transferred call's recording does not continue; `TestRecordingPauseOnHold` fails if recording captures hold silence.
- [ ] [S-5] `TestAnnouncements` passes — fails if an announcement destination does not answer/play/continue, or the before-transfer announcement does not play.
- [ ] [S-6] `TestNATAnchorCarrier` passes — fails if a NAT-simulated trunk call does not anchor and gain working audio where the direct path failed.
- [ ] [S-7] `TestMediaOps` passes — fails if storage usage/anchor counters do not move; a media documentation page under `docs/` exists with the anchoring matrix.
- [ ] [S-1] [S-4] `TestLabSmoke`-equivalent on kw: a recorded call through the deployed instance lands in MinIO and plays from the UI.
- [ ] UI: `procoder test`/`lint` pass over `web/`; `Recordings.test.tsx` fails if a recording cannot be played/deleted, `Announcements.test.tsx` fails if an upload is not listed; `Extensions.test.tsx` fails if the record-default control does not save.

## Open questions

<!-- All resolved 2026-10-04; answers in .procoder/ask/answers.md and folded into Source and In scope. -->
