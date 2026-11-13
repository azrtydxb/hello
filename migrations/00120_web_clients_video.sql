-- +goose Up
-- Web softphone and web video (clients-webrtc-mobile-video S-18 to S-20,
-- S-36). Additive, so a rolling upgrade works.
--
-- extensions.video_allowed: an extension whose calls may carry video between
-- web clients (default on); false answers every video section with port 0.
-- cdrs.video: the call carried video at some point.
-- cdr_client_quality: the end-of-call summary a web client posts (codec, loss,
-- jitter, RTT, candidate types, video resolution; never media). The report
-- names the client's own dialog, one leg of a B2BUA call, and may arrive
-- before the CDR is written, so, like cdr_rtcpxr, it is matched to the CDR when
-- read: by sip_call_id or peer_call_id.

ALTER TABLE extensions ADD COLUMN video_allowed BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE cdrs ADD COLUMN video BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE cdr_client_quality (
    call_id      TEXT        NOT NULL CHECK (length(call_id) BETWEEN 1 AND 256),
    device_id    BIGINT      NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    user_id      BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    codec        TEXT        NOT NULL DEFAULT '' CHECK (length(codec) <= 32),
    loss_pct     REAL        NOT NULL DEFAULT 0 CHECK (loss_pct BETWEEN 0 AND 100),
    jitter_ms    REAL        NOT NULL DEFAULT 0 CHECK (jitter_ms >= 0),
    rtt_ms       REAL        NOT NULL DEFAULT 0 CHECK (rtt_ms >= 0),
    candidate_local  TEXT    NOT NULL DEFAULT '' CHECK (length(candidate_local) <= 16),
    candidate_remote TEXT    NOT NULL DEFAULT '' CHECK (length(candidate_remote) <= 16),
    video_width  INT         CHECK (video_width BETWEEN 0 AND 16384),
    video_height INT         CHECK (video_height BETWEEN 0 AND 16384),
    video_fps    INT         CHECK (video_fps BETWEEN 0 AND 1000),
    video_codec  TEXT        CHECK (length(video_codec) <= 32),
    duration_ms  BIGINT      NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    reported_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (call_id, device_id)
);
CREATE INDEX cdr_client_quality_reported_at_idx ON cdr_client_quality (reported_at);

-- +goose Down
DROP TABLE cdr_client_quality;
ALTER TABLE cdrs DROP COLUMN video;
ALTER TABLE extensions DROP COLUMN video_allowed;
