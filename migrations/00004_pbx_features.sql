-- +goose Up
-- Phase 4: voicemail, ring/hunt groups, per-extension features, feature codes.

CREATE TABLE voicemail_boxes (
    id            BIGSERIAL   PRIMARY KEY,
    extension_id  BIGINT      NOT NULL UNIQUE REFERENCES extensions (id) ON DELETE CASCADE,
    password_hash TEXT        NOT NULL DEFAULT '',
    email         TEXT        NOT NULL DEFAULT '',
    greeting_object       TEXT NOT NULL DEFAULT '',
    unreachable_object    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE voicemail_messages (
    id           BIGSERIAL   PRIMARY KEY,
    box_id       BIGINT      NOT NULL REFERENCES voicemail_boxes (id) ON DELETE CASCADE,
    minio_object TEXT        NOT NULL,
    caller       TEXT        NOT NULL,
    duration_ms  BIGINT      NOT NULL,
    heard        BOOLEAN     NOT NULL DEFAULT FALSE,
    email_status TEXT        NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX voicemail_messages_box ON voicemail_messages (box_id, created_at DESC);

CREATE TABLE ring_groups (
    id             BIGSERIAL   PRIMARY KEY,
    name           TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    strategy       TEXT        NOT NULL CHECK (strategy IN ('ring-all', 'sequential', 'round-robin', 'longest-idle', 'weighted')),
    hunt           BOOLEAN     NOT NULL DEFAULT FALSE,
    ring_timeout   INTEGER     NOT NULL DEFAULT 30 CHECK (ring_timeout BETWEEN 5 AND 300),
    member_delay   INTEGER     NOT NULL DEFAULT 5 CHECK (member_delay BETWEEN 0 AND 60),
    ignore_dnd     BOOLEAN     NOT NULL DEFAULT FALSE,
    failure_kind   TEXT        NOT NULL DEFAULT 'none' CHECK (failure_kind IN ('none', 'voicemail', 'external')),
    failure_target TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ring_group_members (
    group_id     BIGINT  NOT NULL REFERENCES ring_groups (id) ON DELETE CASCADE,
    extension_id BIGINT  NOT NULL REFERENCES extensions (id),
    position     INTEGER NOT NULL CHECK (position >= 1),
    weight       INTEGER NOT NULL DEFAULT 1 CHECK (weight > 0),
    delay        INTEGER NOT NULL DEFAULT 0 CHECK (delay >= 0),
    PRIMARY KEY (group_id, extension_id),
    UNIQUE (group_id, position) DEFERRABLE INITIALLY DEFERRED
);

ALTER TABLE extensions
    ADD COLUMN dnd               BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN forward_always    TEXT    NOT NULL DEFAULT '',
    ADD COLUMN forward_busy      TEXT    NOT NULL DEFAULT '',
    ADD COLUMN forward_no_answer TEXT    NOT NULL DEFAULT '',
    ADD COLUMN voicemail_enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE feature_codes (
    code     TEXT PRIMARY KEY CHECK (code ~ '^\*[0-9]{2,4}$|^##$'),
    action   TEXT NOT NULL CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off','voicemail','blind_transfer','attended_transfer')),
    argument TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE feature_codes;
ALTER TABLE extensions
    DROP COLUMN voicemail_enabled, DROP COLUMN forward_no_answer,
    DROP COLUMN forward_busy, DROP COLUMN forward_always, DROP COLUMN DND;
DROP TABLE ring_group_members;
DROP TABLE ring_groups;
DROP TABLE voicemail_messages;
DROP TABLE voicemail_boxes;
