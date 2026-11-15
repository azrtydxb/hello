-- +goose Up
-- Messaging media (spec messaging-channels M-18): the attachments of inbox
-- messages, kept in MinIO under msg/ in the existing media bucket, and the
-- short-lived public links a carrier would fetch an outbound MMS's media
-- through once the public listener exists. Everything is additive: no check
-- constraint of an existing table changes, and the tables are empty until a
-- file is sent or received.
--
-- An attachment row outlives its message on purpose: message_id and
-- conversation_id are set NULL when retention or an erase deletes the
-- message, and the media sweeper removes the object from MinIO, checks it
-- is gone and only then drops the row, so no object is ever left without a
-- row that names it.

CREATE TABLE message_media (
    id                  BIGSERIAL   PRIMARY KEY,
    message_id          BIGINT      REFERENCES conversation_messages (id) ON DELETE SET NULL,
    conversation_id     BIGINT      REFERENCES conversations (id) ON DELETE SET NULL,
    channel_id          BIGINT      NOT NULL,
    direction           TEXT        NOT NULL CHECK (direction IN ('in', 'out')),
    status              TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'rejected', 'expired')),
    -- msg/<channel>/<yyyy>/<message>/<sha256>; empty until stored and after
    -- the object is removed.
    object_key          TEXT        NOT NULL DEFAULT '' CHECK (object_key = '' OR object_key LIKE 'msg/%'),
    -- The type sniffed from the bytes (served with it) and the type the
    -- sender declared (advisory).
    content_type        TEXT        NOT NULL DEFAULT '' CHECK (length(content_type) <= 100),
    declared_type       TEXT        NOT NULL DEFAULT '' CHECK (length(declared_type) <= 100),
    size                BIGINT      NOT NULL DEFAULT 0 CHECK (size >= 0),
    sha256              TEXT        NOT NULL DEFAULT '' CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
    filename            TEXT        NOT NULL DEFAULT '' CHECK (length(filename) <= 255),
    reason              TEXT        NOT NULL DEFAULT '' CHECK (length(reason) <= 64),
    -- Where an inbound attachment is fetched from (the carrier's URL or id,
    -- its declared type and size). Never shown, never logged.
    source_ref          JSONB       NOT NULL DEFAULT '{}',
    -- Who uploaded an outbound file (an agent) or a chat attachment (a
    -- visitor); an upload is theirs alone until it is sent.
    uploader_user_id    BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    uploader_visitor_id BIGINT      REFERENCES chat_visitors (id) ON DELETE SET NULL,
    attempts            INT         NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when the attachment joins a message. An attached row whose
    -- message_id became NULL is an orphan; an unattached one is an upload
    -- that waits for its message for 24 hours.
    attached_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX message_media_message ON message_media (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX message_media_conversation ON message_media (conversation_id) WHERE conversation_id IS NOT NULL;
CREATE INDEX message_media_fetch ON message_media (next_attempt_at)
    WHERE status = 'pending' AND direction = 'in' AND attached_at IS NOT NULL;
CREATE INDEX message_media_loose ON message_media (created_at) WHERE message_id IS NULL;
CREATE INDEX message_media_ready ON message_media (created_at) WHERE status = 'ready';
CREATE INDEX message_media_visitor ON message_media (uploader_visitor_id) WHERE uploader_visitor_id IS NOT NULL;

-- A public link to an outbound attachment (spec M-18, the pattern of the
-- recording links): only the token's SHA-256 is kept; it works until it
-- expires or is used up.
CREATE TABLE media_links (
    id         BIGSERIAL   PRIMARY KEY,
    token_hash BYTEA       NOT NULL UNIQUE,
    media_id   BIGINT      NOT NULL REFERENCES message_media (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    max_uses   INT         NOT NULL DEFAULT 5 CHECK (max_uses BETWEEN 1 AND 100),
    uses       INT         NOT NULL DEFAULT 0 CHECK (uses >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX media_links_expires ON media_links (expires_at);

-- How long an attachment's file is kept (spec M-19: 90 days by default).
ALTER TABLE messaging_settings ADD COLUMN retention_media_days INT NOT NULL DEFAULT 90
    CHECK (retention_media_days BETWEEN 1 AND 3650);

-- A chat site's attachments switch (off by default, M-29a.6) and its logo.
ALTER TABLE chat_sites ADD COLUMN attachments BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chat_sites ADD COLUMN logo_type TEXT NOT NULL DEFAULT '' CHECK (logo_type IN ('', 'image/png', 'image/jpeg', 'image/gif', 'image/webp'));
ALTER TABLE chat_sites ADD COLUMN logo_updated_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE chat_sites DROP COLUMN logo_updated_at;
ALTER TABLE chat_sites DROP COLUMN logo_type;
ALTER TABLE chat_sites DROP COLUMN attachments;
ALTER TABLE messaging_settings DROP COLUMN retention_media_days;
DROP TABLE media_links;
DROP TABLE message_media;
