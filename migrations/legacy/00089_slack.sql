-- +goose Up
-- Slack notifications (spec integrations P17). A destination is one Slack
-- target: an incoming webhook URL or a bot token plus a channel, sealed with
-- HELLO_SECRET_KEY under the AAD "slack:<id>". Missed calls and voicemail
-- messages are queued in the event ingest transaction (one post per
-- destination and event), and a worker on every control replica claims due
-- rows with FOR UPDATE SKIP LOCKED. Additive: an older node never touches
-- the tables.
CREATE TABLE slack_destinations (
    id                   BIGSERIAL   PRIMARY KEY,
    name                 TEXT        NOT NULL UNIQUE,
    mode                 TEXT        NOT NULL CHECK (mode IN ('webhook', 'bot')),
    secret_enc           BYTEA       NOT NULL,
    -- The channel of a bot destination; an incoming webhook is bound to its
    -- own channel by Slack.
    channel              TEXT        NOT NULL DEFAULT '',
    events               TEXT[]      NOT NULL DEFAULT '{call.missed,voicemail.received}',
    -- Only events about these extensions; empty is every extension.
    extensions           TEXT[]      NOT NULL DEFAULT '{}',
    enabled              BOOLEAN     NOT NULL DEFAULT TRUE,
    disabled_reason      TEXT        NOT NULL DEFAULT '',
    consecutive_failures INT         NOT NULL DEFAULT 0,
    last_success_at      TIMESTAMPTZ,
    last_failure_at      TIMESTAMPTZ,
    last_error           TEXT        NOT NULL DEFAULT '',
    created_by           TEXT        NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE slack_posts (
    id              BIGSERIAL   PRIMARY KEY,
    destination_id  BIGINT      NOT NULL REFERENCES slack_destinations (id) ON DELETE CASCADE,
    event_id        TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'retrying', 'succeeded', 'dead')),
    attempts        INT         NOT NULL DEFAULT 0,
    -- Attempts since the last manual retry: it picks the retry delay.
    series_attempt  INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    claimed_until   TIMESTAMPTZ,
    last_error      TEXT        NOT NULL DEFAULT '',
    delivered_at    TIMESTAMPTZ,
    -- The event as the ingest stored it (names and numbers, no audio).
    payload         JSONB       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (destination_id, event_id)
);
CREATE INDEX slack_posts_due ON slack_posts (next_attempt_at) WHERE status IN ('pending', 'retrying');
CREATE INDEX slack_posts_list ON slack_posts (destination_id, id DESC);

-- +goose Down
DROP TABLE slack_posts;
DROP TABLE slack_destinations;
