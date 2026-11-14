-- +goose Up
-- Video recording (clients-webrtc-mobile-video S-42). Additive, so a rolling
-- upgrade works.
--
-- extensions.record_video: an extension whose recorded calls keep video (on by
--   default); false records the audio only.
-- recording_settings: one row, the video retention (audio keeps no retention
--   job today, so this is the first setting of the table).
-- recording_streams: one row per recorded video stream of a call (a sending
--   leg and a part), written by hello-sip while the stream records and finished
--   by hello-control. Fragments and the init segment sit in the recordings
--   bucket under object_prefix; the finalizer joins them into final_object.
--   segment_list is what the stored fragments are (object, bytes, ticks, base),
--   so a stream whose node died can still be finalized from the row.

ALTER TABLE extensions ADD COLUMN record_video BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE recording_settings (
    id                   SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    video_retention_days INT         NOT NULL DEFAULT 30 CHECK (video_retention_days BETWEEN 1 AND 3650),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO recording_settings (id) VALUES (1);

CREATE TABLE recording_streams (
    id               BIGSERIAL   PRIMARY KEY,
    call_correlation TEXT        NOT NULL CHECK (length(call_correlation) BETWEEN 1 AND 256),
    part             INT         NOT NULL CHECK (part >= 1),
    leg              TEXT        NOT NULL CHECK (leg IN ('caller', 'callee')),
    direction        TEXT        NOT NULL DEFAULT 'in' CHECK (direction IN ('in')),
    codec            TEXT        NOT NULL CHECK (codec IN ('h264', 'vp8')),
    container        TEXT        NOT NULL CHECK (container IN ('mp4', 'webm')),
    object_prefix    TEXT        NOT NULL CHECK (length(object_prefix) BETWEEN 1 AND 512),
    segments         INT         NOT NULL DEFAULT 0 CHECK (segments >= 0),
    bytes            BIGINT      NOT NULL DEFAULT 0 CHECK (bytes >= 0),
    width            INT         NOT NULL DEFAULT 0 CHECK (width BETWEEN 0 AND 16384),
    height           INT         NOT NULL DEFAULT 0 CHECK (height BETWEEN 0 AND 16384),
    offset_ms        BIGINT      NOT NULL DEFAULT 0,
    duration_ms      BIGINT      NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    segment_list     JSONB       NOT NULL DEFAULT '[]',
    final_object     TEXT,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at         TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    state            TEXT        NOT NULL DEFAULT 'recording'
                     CHECK (state IN ('recording', 'ended', 'finalized', 'failed', 'purged')),
    failure          TEXT        NOT NULL DEFAULT '' CHECK (length(failure) <= 64),
    node             TEXT        NOT NULL DEFAULT '' CHECK (length(node) <= 128),
    claim_until      TIMESTAMPTZ,
    UNIQUE (call_correlation, part, leg)
);
CREATE INDEX recording_streams_state_idx ON recording_streams (state, updated_at);
CREATE INDEX recording_streams_ended_idx ON recording_streams (ended_at) WHERE state <> 'recording';

-- +goose Down
DROP TABLE recording_streams;
DROP TABLE recording_settings;
ALTER TABLE extensions DROP COLUMN record_video;
