-- +goose Up
-- Phase 1: management users, extensions/devices, audit, CDRs.
-- Owned by hello-control; hello-sip reads extensions/devices and inserts cdrs.

CREATE TABLE users (
    id            BIGSERIAL   PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL, -- bcrypt
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash BYTEA       PRIMARY KEY, -- SHA-256 of the cookie value
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

CREATE TABLE api_tokens (
    id           BIGSERIAL   PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    token_hash   BYTEA       NOT NULL UNIQUE, -- SHA-256 of the bearer token
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE extensions (
    id         BIGSERIAL   PRIMARY KEY,
    number     TEXT        NOT NULL UNIQUE CHECK (number ~ '^[0-9]{2,10}$'),
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE devices (
    id           BIGSERIAL   PRIMARY KEY,
    extension_id BIGINT      NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    sip_username TEXT        NOT NULL UNIQUE CHECK (sip_username ~ '^[A-Za-z0-9._-]{1,64}$'),
    -- Digest HA1 = H(username:realm:secret); the secret itself is never stored.
    -- realm is the SIP domain the HA1 values were computed for.
    realm        TEXT        NOT NULL,
    ha1_md5      TEXT        NOT NULL,
    ha1_sha256   TEXT        NOT NULL,
    enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX devices_extension_id ON devices (extension_id);

CREATE TABLE audit_events (
    id          BIGSERIAL   PRIMARY KEY,
    actor       TEXT        NOT NULL, -- "user:<username>" or "token:<id>"
    action      TEXT        NOT NULL, -- create | update | delete | rotate-secret | login | ...
    resource    TEXT        NOT NULL, -- extension | device | user | api_token
    resource_id TEXT        NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE cdrs (
    id               BIGSERIAL   PRIMARY KEY,
    correlation_id   TEXT        NOT NULL UNIQUE,
    sip_call_id      TEXT        NOT NULL,
    source           TEXT        NOT NULL,
    destination      TEXT        NOT NULL,
    start_time       TIMESTAMPTZ NOT NULL,
    ring_time        TIMESTAMPTZ,
    answer_time      TIMESTAMPTZ,
    end_time         TIMESTAMPTZ NOT NULL,
    duration_ms      BIGINT      NOT NULL,
    billable_ms      BIGINT      NOT NULL,
    sip_node         TEXT        NOT NULL,
    media_mode       TEXT        NOT NULL DEFAULT 'direct',
    final_status     INTEGER     NOT NULL,
    termination_side TEXT        NOT NULL, -- caller | callee | system
    failure_reason   TEXT        NOT NULL DEFAULT ''
);
-- CDRs are paged by id (newest first), which the primary key serves.

-- +goose Down
DROP TABLE cdrs;
DROP TABLE audit_events;
DROP TABLE devices;
DROP TABLE extensions;
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE users;
