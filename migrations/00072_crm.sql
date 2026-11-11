-- +goose Up
-- CRM connectors (spec integrations I-47, I-48, I-53, I-54). A connector is
-- one CRM the customer links (its own client id and secret, sealed); a
-- connection holds the sealed OAuth tokens; call logs are the durable jobs
-- that write calls into the CRM; the user map ties a Hello user to the CRM
-- user that owns their calls.
CREATE TABLE crm_connectors (
    id                BIGSERIAL   PRIMARY KEY,
    kind              TEXT        NOT NULL,
    name              TEXT        NOT NULL UNIQUE,
    enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
    config            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    client_secret_enc BYTEA,
    recordings        BOOLEAN     NOT NULL DEFAULT FALSE,
    link_ttl_hours    INTEGER     NOT NULL DEFAULT 720 CHECK (link_ttl_hours BETWEEN 1 AND 8760),
    log_missed        BOOLEAN     NOT NULL DEFAULT TRUE,
    -- fallback_owner is the CRM user id that owns the calls of a user with
    -- no mapping; empty skips those calls.
    fallback_owner    TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE crm_connections (
    id               BIGSERIAL   PRIMARY KEY,
    connector_id     BIGINT      NOT NULL REFERENCES crm_connectors (id) ON DELETE CASCADE,
    scope            TEXT        NOT NULL CHECK (scope IN ('org', 'user')),
    user_id          BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    access_enc       BYTEA,
    refresh_enc      BYTEA,
    expires_at       TIMESTAMPTZ,
    granted_scopes   TEXT        NOT NULL DEFAULT '',
    external_account TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'connected' CHECK (status IN ('connected', 'needs_reauth', 'revoked')),
    last_error       TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((scope = 'user') = (user_id IS NOT NULL))
);
CREATE UNIQUE INDEX crm_connections_org ON crm_connections (connector_id) WHERE scope = 'org';
CREATE UNIQUE INDEX crm_connections_user ON crm_connections (connector_id, user_id) WHERE scope = 'user';

-- crm_oauth_states are the single-use states of an OAuth authorization in
-- flight; only the SHA-256 of the state is stored.
CREATE TABLE crm_oauth_states (
    state_hash   BYTEA       PRIMARY KEY,
    connector_id BIGINT      NOT NULL REFERENCES crm_connectors (id) ON DELETE CASCADE,
    actor        TEXT        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX crm_oauth_states_expires ON crm_oauth_states (expires_at);

-- crm_call_logs: one row per (connector, call). waiting rows have no call
-- payload yet (the recording or the iframe's engagement arrived first);
-- pending rows are due at next_attempt_at; logged rows are in the CRM;
-- update rows are logged and need a patch (a recording link); dead rows ran
-- out of attempts; skipped rows were deliberately not logged.
CREATE TABLE crm_call_logs (
    id               BIGSERIAL   PRIMARY KEY,
    connector_id     BIGINT      NOT NULL REFERENCES crm_connectors (id) ON DELETE CASCADE,
    call_id          TEXT        NOT NULL,
    direction        TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL CHECK (status IN ('waiting', 'pending', 'logged', 'update', 'dead', 'skipped')),
    external_id      TEXT        NOT NULL DEFAULT '',
    attempts         INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error       TEXT        NOT NULL DEFAULT '',
    payload          JSONB,
    event_type       TEXT        NOT NULL DEFAULT '',
    owner_extension  TEXT        NOT NULL DEFAULT '',
    recording_id     BIGINT,
    link_expires_at  TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, call_id)
);
CREATE INDEX crm_call_logs_due ON crm_call_logs (next_attempt_at) WHERE status IN ('pending', 'update');
CREATE INDEX crm_call_logs_renew ON crm_call_logs (link_expires_at) WHERE link_expires_at IS NOT NULL;

CREATE TABLE crm_user_map (
    connector_id     BIGINT NOT NULL REFERENCES crm_connectors (id) ON DELETE CASCADE,
    user_id          BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    external_user_id TEXT   NOT NULL,
    source           TEXT   NOT NULL DEFAULT 'email' CHECK (source IN ('email', 'manual')),
    PRIMARY KEY (connector_id, user_id)
);

-- +goose Down
DROP TABLE crm_user_map;
DROP TABLE crm_call_logs;
DROP TABLE crm_oauth_states;
DROP TABLE crm_connections;
DROP TABLE crm_connectors;
