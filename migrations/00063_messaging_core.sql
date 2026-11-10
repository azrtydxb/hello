-- +goose Up
-- Messaging channels, phase 1 (spec messaging-channels, "inbox core"): the
-- unified inbox. A channel is where customer texts come from (today a SIP
-- trunk that carries SMS as SIP MESSAGE), an address is one number on a
-- channel, a conversation is one customer on one address, and a message is
-- one line of it. Everything is additive, so a rolling upgrade works: the
-- tables are empty until an administrator adds a channel, and the SIP text
-- messaging of numbers that are not channel addresses is unchanged.
--
-- Message bodies are plain text (database volume encryption is the
-- platform's job) with a full-text index. Credentials are sealed with the
-- secret box and never returned by the API. Nothing here holds state that is
-- only in memory: a control or SIP pod restart loses nothing.

CREATE TABLE msg_channels (
    id             BIGSERIAL   PRIMARY KEY,
    kind           TEXT        NOT NULL CHECK (kind IN ('sms', 'whatsapp', 'webchat')),
    -- sip_trunk today; the carrier adapters of later phases add their own.
    provider       TEXT        NOT NULL CHECK (length(provider) BETWEEN 1 AND 32),
    name           TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    -- Routing key of the webhook URL of a later phase; not an authenticator.
    hook_id        TEXT        NOT NULL UNIQUE,
    -- The SIP trunk a sip_trunk channel reads texts from and sends through.
    trunk_id       BIGINT      REFERENCES trunks (id) ON DELETE SET NULL,
    config         JSONB       NOT NULL DEFAULT '{}',
    credentials_enc BYTEA,
    credential_fp  TEXT        NOT NULL DEFAULT '',
    status         TEXT        NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'degraded', 'credentials_invalid')),
    status_detail  TEXT        NOT NULL DEFAULT '',
    last_event_at  TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX msg_channels_name ON msg_channels (lower(name));
CREATE INDEX msg_channels_trunk ON msg_channels (trunk_id) WHERE trunk_id IS NOT NULL;

CREATE TABLE msg_addresses (
    id              BIGSERIAL   PRIMARY KEY,
    channel_id      BIGINT      NOT NULL REFERENCES msg_channels (id) ON DELETE CASCADE,
    -- The number as an administrator typed it, and its lookup key (the same
    -- reduction the address books use), which inbound texts are matched by.
    address         TEXT        NOT NULL CHECK (length(address) BETWEEN 2 AND 64),
    address_key     TEXT        NOT NULL,
    -- The last nine digits of the key (empty for a shorter number): a carrier
    -- that dials the number without its + still finds it.
    address_suffix  TEXT        NOT NULL DEFAULT '',
    label           TEXT        NOT NULL DEFAULT '' CHECK (length(label) <= 100),
    -- Where a new conversation goes. inbox is the unassigned pool every
    -- operator sees; later phases add queue, user and the other kinds.
    route_kind      TEXT        NOT NULL DEFAULT 'inbox' CHECK (route_kind IN ('inbox')),
    route_target    TEXT        NOT NULL DEFAULT '',
    -- Opt-out handling (Hello does it itself, whatever the carrier does).
    optout_keywords TEXT[]      NOT NULL DEFAULT '{stop,stopall,unsubscribe,cancel,end,quit}',
    help_text       TEXT        NOT NULL DEFAULT '' CHECK (length(help_text) <= 480),
    optout_text     TEXT        NOT NULL DEFAULT '' CHECK (length(optout_text) <= 480),
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, address_key)
);
CREATE INDEX msg_addresses_key ON msg_addresses (address_key);
CREATE INDEX msg_addresses_suffix ON msg_addresses (address_suffix) WHERE address_suffix <> '';

CREATE TABLE conversations (
    id                BIGSERIAL   PRIMARY KEY,
    channel_id        BIGINT      NOT NULL REFERENCES msg_channels (id) ON DELETE CASCADE,
    address_id        BIGINT      NOT NULL REFERENCES msg_addresses (id) ON DELETE CASCADE,
    -- The peer's number (or a chat visitor id) reduced to its lookup key.
    peer_key          TEXT        NOT NULL,
    peer_kind         TEXT        NOT NULL DEFAULT 'phone' CHECK (peer_kind IN ('phone', 'visitor')),
    -- Only a name from a shared source (never from a personal book).
    peer_name         TEXT        NOT NULL DEFAULT '',
    peer_name_source  TEXT        NOT NULL DEFAULT '' CHECK (peer_name_source IN ('', 'extensions', 'company', 'named', 'visitor', 'provider')),
    status            TEXT        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'pending', 'snoozed', 'resolved', 'spam')),
    snoozed_until     TIMESTAMPTZ,
    assignee_user_id  BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    priority          TEXT        NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high')),
    tags              TEXT[]      NOT NULL DEFAULT '{}',
    last_message_id   BIGINT,
    last_message_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_inbound_at   TIMESTAMPTZ,
    first_response_at TIMESTAMPTZ,
    resolved_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One open conversation per address and peer: two pods creating the same
-- conversation race on this index and one attaches to the other's row.
CREATE UNIQUE INDEX conversations_open_peer ON conversations (address_id, peer_key)
    WHERE status NOT IN ('resolved', 'spam');
CREATE INDEX conversations_assignee ON conversations (assignee_user_id, status, last_message_at DESC);
CREATE INDEX conversations_pool ON conversations (status, last_message_at) WHERE assignee_user_id IS NULL;
CREATE INDEX conversations_peer ON conversations (peer_key);
CREATE INDEX conversations_recent ON conversations (last_message_at DESC, id DESC);
CREATE INDEX conversations_snoozed ON conversations (snoozed_until) WHERE status = 'snoozed';

CREATE TABLE conversation_messages (
    id                  BIGSERIAL   PRIMARY KEY,
    conversation_id     BIGINT      NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    channel_id          BIGINT      NOT NULL,
    direction           TEXT        NOT NULL CHECK (direction IN ('in', 'out')),
    -- message goes to the customer; note is internal and never sent;
    -- system is a line the inbox wrote (assigned, resolved, opted out).
    kind                TEXT        NOT NULL DEFAULT 'message' CHECK (kind IN ('message', 'note', 'system')),
    sender_kind         TEXT        NOT NULL CHECK (sender_kind IN ('customer', 'user', 'ai', 'system')),
    sender_user_id      BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    -- The sender's display name when the message was written.
    sender_name         TEXT        NOT NULL DEFAULT '',
    body                TEXT        NOT NULL,
    content_type        TEXT        NOT NULL DEFAULT 'text/plain',
    status              TEXT        NOT NULL DEFAULT 'delivered' CHECK (status IN ('queued', 'sending', 'sent', 'delivered', 'read', 'failed', 'unknown')),
    error_code          TEXT        NOT NULL DEFAULT '',
    provider_message_id TEXT,
    client_msg_id       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at             TIMESTAMPTZ,
    delivered_at        TIMESTAMPTZ,
    read_at             TIMESTAMPTZ,
    UNIQUE (conversation_id, client_msg_id),
    UNIQUE (channel_id, provider_message_id)
);
CREATE INDEX conversation_messages_thread ON conversation_messages (conversation_id, id DESC);
CREATE INDEX conversation_messages_search ON conversation_messages USING GIN (to_tsvector('simple', body));
CREATE INDEX conversation_messages_open ON conversation_messages (id) WHERE status IN ('queued', 'sending');

-- Inbound idempotency ledger: an event key seen once is never ingested
-- again, whichever pod or retry delivers it. Kept 7 days.
CREATE TABLE messaging_events (
    channel_id  BIGINT      NOT NULL REFERENCES msg_channels (id) ON DELETE CASCADE,
    event_key   TEXT        NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, event_key)
);
CREATE INDEX messaging_events_received ON messaging_events (received_at);

-- Opt-outs. peer_hash is HMAC-SHA256 of the peer's key under a key derived
-- from HELLO_SECRET_KEY; it is all that remains of an erased number, so a
-- deleted customer is not texted again.
CREATE TABLE sms_opt_outs (
    id        BIGSERIAL   PRIMARY KEY,
    address_id BIGINT     NOT NULL REFERENCES msg_addresses (id) ON DELETE CASCADE,
    peer_key  TEXT,
    peer_hash BYTEA       NOT NULL,
    state     TEXT        NOT NULL CHECK (state IN ('out', 'in')),
    source    TEXT        NOT NULL CHECK (source IN ('keyword', 'provider', 'admin', 'customer')),
    note      TEXT        NOT NULL DEFAULT '',
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (address_id, peer_hash)
);

CREATE TABLE conversation_reads (
    user_id              BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    conversation_id      BIGINT      NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    last_read_message_id BIGINT      NOT NULL DEFAULT 0,
    at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, conversation_id)
);

CREATE TABLE conversation_assignments (
    id              BIGSERIAL   PRIMARY KEY,
    conversation_id BIGINT      NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    from_user_id    BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    to_user_id      BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    reason          TEXT        NOT NULL DEFAULT '',
    at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX conversation_assignments_conv ON conversation_assignments (conversation_id, id);

-- The live-update log: one row per user an event concerns, so a stream
-- resumes from Last-Event-ID on any pod. Ids only, never content. Kept 7
-- days. notified is the email worker's mark.
CREATE TABLE conversation_events (
    id              BIGSERIAL   PRIMARY KEY,
    user_id         BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    type            TEXT        NOT NULL,
    conversation_id BIGINT      NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    message_id      BIGINT,
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    notified        BOOLEAN     NOT NULL DEFAULT FALSE
);
CREATE INDEX conversation_events_user ON conversation_events (user_id, id);
CREATE INDEX conversation_events_at ON conversation_events (at);
CREATE INDEX conversation_events_unnotified ON conversation_events (at) WHERE NOT notified;

-- When a user's inbox stream was last open, for the email fallback.
CREATE TABLE inbox_presence (
    user_id BIGINT      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One email per user and conversation per 15 minutes.
CREATE TABLE conversation_notify (
    user_id         BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    conversation_id BIGINT      NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    last_email_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, conversation_id)
);

-- Administrator settings (singleton row).
CREATE TABLE messaging_settings (
    id                  BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    -- How long a finished conversation is kept, per channel kind. Open
    -- conversations are never purged by age.
    retention_sms_days  INT     NOT NULL DEFAULT 365 CHECK (retention_sms_days BETWEEN 1 AND 3650),
    retention_chat_days INT     NOT NULL DEFAULT 90 CHECK (retention_chat_days BETWEEN 1 AND 3650),
    -- A new text on a resolved conversation reopens it within this many days.
    reopen_days         INT     NOT NULL DEFAULT 7 CHECK (reopen_days BETWEEN 0 AND 365),
    -- Email an agent who has no inbox open (never with the message text).
    email_notify        BOOLEAN NOT NULL DEFAULT TRUE
);
INSERT INTO messaging_settings DEFAULT VALUES;

-- The SIP text queue row an inbox reply produced, so its delivery result
-- reaches the conversation (sent when the trunk accepted it, failed when it
-- refused or expired).
ALTER TABLE messages ADD COLUMN conversation_message_id BIGINT REFERENCES conversation_messages (id) ON DELETE SET NULL;
CREATE INDEX messages_conversation_message ON messages (conversation_message_id) WHERE conversation_message_id IS NOT NULL;

-- +goose Down
DROP INDEX messages_conversation_message;
ALTER TABLE messages DROP COLUMN conversation_message_id;
DROP TABLE messaging_settings;
DROP TABLE conversation_notify;
DROP TABLE inbox_presence;
DROP TABLE conversation_events;
DROP TABLE conversation_assignments;
DROP TABLE conversation_reads;
DROP TABLE sms_opt_outs;
DROP TABLE messaging_events;
DROP TABLE conversation_messages;
DROP TABLE conversations;
DROP TABLE msg_addresses;
DROP TABLE msg_channels;
