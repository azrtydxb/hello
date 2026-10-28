-- +goose Up
-- Click-to-call (spec integrations P3, I-37 to I-39). hello-control records
-- every originate request here and queues it for a hello-sip node; the live
-- state of a request is in Valkey while it runs and is copied back here when
-- it ends. Additive: an older node never touches the table or the columns.

CREATE TABLE originate_requests (
    id                 TEXT        PRIMARY KEY,
    idempotency_key    TEXT        NOT NULL DEFAULT '',
    -- The extension that rings first, and the number it is to call.
    extension_number   TEXT        NOT NULL,
    destination        TEXT        NOT NULL,
    caller_id          TEXT        NOT NULL DEFAULT '',
    -- Who asked: the OAuth client or token id, and the acting user or
    -- service account (audit actor).
    client             TEXT        NOT NULL DEFAULT '',
    actor              TEXT        NOT NULL DEFAULT '',
    -- queued, ringing_agent, agent_answered, dialing_target, connected,
    -- ended or failed.
    state              TEXT        NOT NULL DEFAULT 'queued',
    reason             TEXT        NOT NULL DEFAULT '',
    cdr_correlation_id TEXT        NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX originate_requests_open ON originate_requests (updated_at)
    WHERE state NOT IN ('ended', 'failed');
CREATE INDEX originate_requests_created ON originate_requests (created_at);

-- The administrator's click-to-call policy (singleton row): which
-- destinations may be dialled by the API and whether a request may ask the
-- phone to answer by itself. Both default to the safe choice.
CREATE TABLE originate_settings (
    id                  BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    -- inherit: the outbound routes decide. restricted: internal numbers
    -- plus numbers that start with one of country_prefixes.
    destinations        TEXT    NOT NULL DEFAULT 'inherit' CHECK (destinations IN ('inherit', 'restricted')),
    country_prefixes    TEXT[]  NOT NULL DEFAULT '{}',
    auto_answer_allowed BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO originate_settings DEFAULT VALUES;

-- Where a call came from, so reports separate click-to-call from a call
-- dialled at the phone: origin is phone or api, origin_client the OAuth
-- client or token id behind an api call.
ALTER TABLE cdrs
    ADD COLUMN origin        TEXT NOT NULL DEFAULT 'phone' CHECK (origin IN ('phone', 'api')),
    ADD COLUMN origin_client TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE cdrs DROP COLUMN origin_client, DROP COLUMN origin;
DROP TABLE originate_settings;
DROP TABLE originate_requests;
