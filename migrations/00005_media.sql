-- +goose Up
-- Phase 5: call recordings, announcements, per-extension record default.

CREATE TABLE recordings (
    id            BIGSERIAL   PRIMARY KEY,
    correlation_id TEXT       NOT NULL UNIQUE,
    minio_object  TEXT        NOT NULL,
    initiated_by  TEXT        NOT NULL CHECK (initiated_by IN ('dtmf','default','api')),
    duration_ms   BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE announcements (
    id           BIGSERIAL   PRIMARY KEY,
    name         TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    minio_object TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE extensions ADD COLUMN record_default BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
  CHECK (failure_kind IN ('none','voicemail','external','announcement'));

ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement'));

-- +goose Down
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer'));
ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
  CHECK (failure_kind IN ('none','voicemail','external'));
ALTER TABLE extensions DROP COLUMN record_default;
DROP TABLE announcements;
DROP TABLE recordings;
