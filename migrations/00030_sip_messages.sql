-- +goose Up
-- SIP MESSAGE (RFC 3428) text messaging: extension to extension, to ring and
-- paging groups, system broadcast and, per trunk, SMS over SIP.
-- Additive, so a rolling upgrade works.
--
-- A send is one row per recipient (a "batch" groups the rows of one send).
-- A hello-sip node claims pending rows (status pending -> sending, row lock
-- with SKIP LOCKED, so exactly one node delivers), sends the MESSAGE to the
-- recipient's registered contacts and writes the result. A row whose
-- recipient has no registered device stays pending until a device registers
-- or expires_at passes.

CREATE TABLE messages (
    id              BIGSERIAL   PRIMARY KEY,
    batch           TEXT        NOT NULL,
    -- Who sent it: an extension number, an external number (inbound from a
    -- trunk) or "system" (an administrator, the API or the assistant).
    sender          TEXT        NOT NULL,
    sender_kind     TEXT        NOT NULL CHECK (sender_kind IN ('extension', 'external', 'system')),
    sender_name     TEXT        NOT NULL DEFAULT '',
    actor           TEXT        NOT NULL DEFAULT '',
    recipient       TEXT        NOT NULL,
    recipient_kind  TEXT        NOT NULL CHECK (recipient_kind IN ('extension', 'external')),
    scope           TEXT        NOT NULL DEFAULT 'direct' CHECK (scope IN ('direct', 'ring_group', 'paging_group', 'broadcast')),
    scope_ref       TEXT        NOT NULL DEFAULT '',
    -- The carrier an external message came in on or went out through.
    trunk_id        BIGINT      REFERENCES trunks (id) ON DELETE SET NULL,
    content_type    TEXT        NOT NULL DEFAULT 'text/plain' CHECK (length(content_type) BETWEEN 1 AND 128),
    -- Valid UTF-8 text goes in body_text (searchable); anything else is
    -- relayed byte for byte from body_raw.
    body_text       TEXT,
    body_raw        BYTEA,
    CHECK ((body_text IS NULL) <> (body_raw IS NULL)),
    status          TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'delivered', 'failed')),
    detail          TEXT        NOT NULL DEFAULT '',
    attempts        INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at      TIMESTAMPTZ,
    claimed_by      TEXT        NOT NULL DEFAULT '',
    delivered_at    TIMESTAMPTZ
);
CREATE INDEX messages_recipient ON messages (recipient, id DESC);
CREATE INDEX messages_sender ON messages (sender, id DESC);
CREATE INDEX messages_batch ON messages (batch);
CREATE INDEX messages_created ON messages (created_at);
CREATE INDEX messages_pending ON messages (recipient_kind, recipient, id) WHERE status = 'pending';
CREATE INDEX messages_sending ON messages (claimed_at) WHERE status = 'sending';

-- Administrator settings (singleton row).
CREATE TABLE message_settings (
    id                       BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    enabled                  BOOLEAN NOT NULL DEFAULT TRUE,
    -- How long an undelivered message waits for its recipient to register.
    ttl_hours                INT     NOT NULL DEFAULT 24 CHECK (ttl_hours BETWEEN 1 AND 720),
    -- How long the history is kept.
    retention_days           INT     NOT NULL DEFAULT 30 CHECK (retention_days BETWEEN 1 AND 3650),
    -- Sends per sender per minute.
    rate_per_minute          INT     NOT NULL DEFAULT 30 CHECK (rate_per_minute BETWEEN 1 AND 1000),
    -- Undelivered messages kept per extension; the oldest are dropped.
    max_queued_per_extension INT     NOT NULL DEFAULT 100 CHECK (max_queued_per_extension BETWEEN 1 AND 10000)
);
INSERT INTO message_settings DEFAULT VALUES;

-- SMS over SIP is off for every trunk until an administrator turns it on.
ALTER TABLE trunks ADD COLUMN messaging BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE trunks DROP COLUMN messaging;
DROP TABLE message_settings;
DROP TABLE messages;
