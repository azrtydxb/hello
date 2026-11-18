-- +goose Up
-- Messaging channels, the LAN chat widget (spec messaging-channels M-28 to
-- M-29c): a chat site is a channel of kind webchat with one address, and a
-- visitor is an anonymous peer. Everything is additive: the tables are empty
-- until an administrator adds a site, and nothing here changes the SIP text
-- path. A site's key is public (96 random bits, not a secret); a visitor's
-- token never is stored, only its SHA-256.
--
-- No check constraint of an existing table changes: msg_channels already
-- allows kind webchat, conversations already allow the visitor peer kind and
-- the visitor name source.

CREATE TABLE chat_sites (
    id                   BIGSERIAL   PRIMARY KEY,
    channel_id           BIGINT      NOT NULL UNIQUE REFERENCES msg_channels (id) ON DELETE CASCADE,
    address_id           BIGINT      NOT NULL UNIQUE REFERENCES msg_addresses (id) ON DELETE CASCADE,
    site_key             TEXT        NOT NULL UNIQUE CHECK (site_key ~ '^[0-9a-f]{24}$'),
    -- LAN origins that may frame the widget (frame-ancestors); empty means
    -- none until configured.
    allowed_origins      TEXT[]      NOT NULL DEFAULT '{}',
    greeting             TEXT        NOT NULL DEFAULT '' CHECK (length(greeting) <= 500),
    accent               TEXT        NOT NULL DEFAULT '#2563eb' CHECK (accent ~ '^#[0-9a-fA-F]{6}$'),
    position             TEXT        NOT NULL DEFAULT 'right' CHECK (position IN ('left', 'right')),
    -- Whether the widget asks for a name and an email before the first message.
    collect              TEXT        NOT NULL DEFAULT 'off' CHECK (collect IN ('off', 'optional', 'required')),
    -- Weekly opening hours ({"zone": "Europe/Brussels", "days": [{"day": 1, "from": "09:00", "to": "17:00"}]});
    -- empty means always open.
    hours                JSONB       NOT NULL DEFAULT '{}',
    offline_mode         TEXT        NOT NULL DEFAULT 'collect' CHECK (offline_mode IN ('collect', 'hidden')),
    privacy_url          TEXT        NOT NULL DEFAULT '' CHECK (length(privacy_url) <= 500),
    challenge            TEXT        NOT NULL DEFAULT 'pow' CHECK (challenge IN ('pow', 'turnstile', 'none')),
    pow_bits             INT         NOT NULL DEFAULT 16 CHECK (pow_bits BETWEEN 14 AND 22),
    turnstile_site_key   TEXT        NOT NULL DEFAULT '' CHECK (length(turnstile_site_key) <= 100),
    turnstile_secret_enc BYTEA,
    strip_links          BOOLEAN     NOT NULL DEFAULT FALSE,
    max_open_unassigned  INT         NOT NULL DEFAULT 200 CHECK (max_open_unassigned BETWEEN 1 AND 100000),
    transcripts          BOOLEAN     NOT NULL DEFAULT FALSE,
    -- Days a finished chat is kept; NULL follows messaging_settings.
    retention_days       INT         CHECK (retention_days BETWEEN 1 AND 3650),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- An anonymous visitor. The token is shown to the visitor once and only its
-- SHA-256 is kept. The IP is only ever a hash under a salt that changes
-- every UTC day, and the user agent only a coarse family.
CREATE TABLE chat_visitors (
    id               BIGSERIAL   PRIMARY KEY,
    site_id          BIGINT      NOT NULL REFERENCES chat_sites (id) ON DELETE CASCADE,
    token_hash       BYTEA       NOT NULL UNIQUE,
    name             TEXT        NOT NULL DEFAULT '' CHECK (length(name) <= 100),
    email            TEXT        NOT NULL DEFAULT '' CHECK (length(email) <= 254),
    consent_to_store BOOLEAN     NOT NULL DEFAULT FALSE,
    ip_hash          BYTEA,
    ua_family        TEXT        NOT NULL DEFAULT '' CHECK (length(ua_family) <= 32),
    -- Hostname and path of the page the chat started on, no query.
    page             TEXT        NOT NULL DEFAULT '' CHECK (length(page) <= 500),
    first_seen       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX chat_visitors_site ON chat_visitors (site_id, last_seen);

-- A block by visitor and/or by the day's IP hash. The IP hash stops matching
-- at 00:00 UTC when the salt changes, so such a block lasts at most the day.
CREATE TABLE chat_blocks (
    id         BIGSERIAL   PRIMARY KEY,
    site_id    BIGINT      NOT NULL REFERENCES chat_sites (id) ON DELETE CASCADE,
    visitor_id BIGINT      REFERENCES chat_visitors (id) ON DELETE CASCADE,
    ip_hash    BYTEA,
    until      TIMESTAMPTZ NOT NULL,
    by_user_id BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (visitor_id IS NOT NULL OR ip_hash IS NOT NULL)
);
CREATE INDEX chat_blocks_site ON chat_blocks (site_id, until);
CREATE INDEX chat_blocks_visitor ON chat_blocks (visitor_id) WHERE visitor_id IS NOT NULL;

-- What a visitor's stream sends: ids and types, in commit order, so a stream
-- that reconnects with Last-Event-ID on any pod loses and repeats nothing.
-- Kept 7 days.
CREATE TABLE chat_events (
    id         BIGSERIAL   PRIMARY KEY,
    visitor_id BIGINT      NOT NULL REFERENCES chat_visitors (id) ON DELETE CASCADE,
    type       TEXT        NOT NULL,
    message_id BIGINT,
    at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX chat_events_visitor ON chat_events (visitor_id, id);
CREATE INDEX chat_events_at ON chat_events (at);

-- A chat left while no agent was available or outside hours: the visitor gave
-- an address and an agent can answer by email.
ALTER TABLE conversations ADD COLUMN offline BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE conversations DROP COLUMN offline;
DROP TABLE chat_events;
DROP TABLE chat_blocks;
DROP TABLE chat_visitors;
DROP TABLE chat_sites;
