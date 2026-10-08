-- +goose Up
-- Voice agents (spec voice-agents): the registry, versions, MCP servers and
-- attachments, the runtime singleton and per-call reports, and the voice
-- agent as a call destination on inbound routes and ring groups.

CREATE TABLE voice_agents (
    id                        BIGSERIAL   PRIMARY KEY,
    name                      TEXT        NOT NULL UNIQUE
                              CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    description               TEXT        NOT NULL DEFAULT ''
                              CHECK (length(description) <= 2000),
    enabled                   BOOLEAN     NOT NULL DEFAULT TRUE,
    sip_user                  TEXT        NOT NULL UNIQUE
                              CHECK (sip_user ~ '^[0-9]{3,15}$'),
    extension                 TEXT        UNIQUE
                              CHECK (extension IS NULL OR extension ~ '^[0-9*#]{2,20}$'),
    prompt                    TEXT        NOT NULL DEFAULT ''
                              CHECK (length(prompt) <= 32000),
    greeting                  TEXT        NOT NULL DEFAULT ''
                              CHECK (length(greeting) <= 4000),
    language                  TEXT        NOT NULL DEFAULT ''
                              CHECK (length(language) <= 32),
    voice                     TEXT        NOT NULL DEFAULT ''
                              CHECK (length(voice) <= 64),
    voice_reference           TEXT        NOT NULL DEFAULT ''
                              CHECK (length(voice_reference) <= 256),
    style                     TEXT        NOT NULL DEFAULT ''
                              CHECK (length(style) <= 2000),
    temperature               REAL        CHECK (temperature IS NULL OR temperature BETWEEN 0 AND 2),
    max_call_seconds          INTEGER     NOT NULL DEFAULT 600 CHECK (max_call_seconds BETWEEN 30 AND 7200),
    max_concurrent            INTEGER     NOT NULL DEFAULT 1 CHECK (max_concurrent BETWEEN 1 AND 100),
    max_tool_calls            INTEGER     NOT NULL DEFAULT 20 CHECK (max_tool_calls BETWEEN 0 AND 200),
    idle_timeout_seconds      INTEGER     NOT NULL DEFAULT 30 CHECK (idle_timeout_seconds BETWEEN 5 AND 300),
    record_transcript         BOOLEAN     NOT NULL DEFAULT FALSE,
    transcript_retention_days INTEGER     NOT NULL DEFAULT 30 CHECK (transcript_retention_days BETWEEN 1 AND 365),
    caller_verification       TEXT        NOT NULL DEFAULT 'none'
                              CHECK (caller_verification IN ('none', 'allowlist', 'pin', 'allowlist_or_pin')),
    caller_allowlist          JSONB       NOT NULL DEFAULT '[]',
    pin_hash                  TEXT        NOT NULL DEFAULT '',
    revision                  BIGINT      NOT NULL DEFAULT 0 CHECK (revision >= 0),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- persona is the full persona of that revision (spec Data); the registry
-- keeps the last 10 per agent.
CREATE TABLE voice_agent_versions (
    agent_id   BIGINT      NOT NULL REFERENCES voice_agents (id) ON DELETE CASCADE,
    revision   BIGINT      NOT NULL CHECK (revision >= 1),
    persona    JSONB       NOT NULL,
    actor      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, revision)
);

-- credential is sealed by internal/secret (S-17); last_check_status is ok,
-- refused or unreachable.
CREATE TABLE voice_mcp_servers (
    id                BIGSERIAL   PRIMARY KEY,
    name              TEXT        NOT NULL UNIQUE
                      CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    url               TEXT        NOT NULL
                      CHECK (length(url) <= 2000),
    auth              TEXT        NOT NULL
                      CHECK (auth IN ('none', 'bearer', 'header', 'oauth_client_credentials')),
    header_name       TEXT        NOT NULL DEFAULT ''
                      CHECK (length(header_name) <= 128),
    token_url         TEXT        NOT NULL DEFAULT ''
                      CHECK (length(token_url) <= 2000),
    client_id         TEXT        NOT NULL DEFAULT ''
                      CHECK (length(client_id) <= 256),
    oauth_scope       TEXT        NOT NULL DEFAULT ''
                      CHECK (length(oauth_scope) <= 512),
    credential        BYTEA,
    timeout_ms        INTEGER     NOT NULL DEFAULT 5000 CHECK (timeout_ms BETWEEN 100 AND 30000),
    enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
    last_check_at     TIMESTAMPTZ,
    last_check_status TEXT        CHECK (last_check_status IS NULL OR last_check_status IN ('ok', 'refused', 'unreachable'))
);

-- tools is [{name, confirm, write}] (contract with talking-agent, S-6);
-- deleting a server an agent still names is refused (restrict), like a route
-- naming a deleted agent.
CREATE TABLE voice_agent_mcp (
    agent_id  BIGINT  NOT NULL REFERENCES voice_agents (id) ON DELETE CASCADE,
    server_id BIGINT  NOT NULL REFERENCES voice_mcp_servers (id) ON DELETE RESTRICT,
    enabled   BOOLEAN NOT NULL DEFAULT TRUE,
    tools     JSONB   NOT NULL DEFAULT '[]',
    PRIMARY KEY (agent_id, server_id)
);

-- A ring group member is an extension or a voice agent, never both, never
-- neither. The old primary key (group_id, extension_id) cannot survive a
-- nullable extension_id; one agent per group is unique like one extension
-- per group was.
ALTER TABLE ring_group_members DROP CONSTRAINT ring_group_members_pkey;
ALTER TABLE ring_group_members ALTER COLUMN extension_id DROP NOT NULL;
ALTER TABLE ring_group_members ADD COLUMN voice_agent_id BIGINT REFERENCES voice_agents (id) ON DELETE RESTRICT;
ALTER TABLE ring_group_members ADD CONSTRAINT ring_group_members_target CHECK (num_nonnulls(extension_id, voice_agent_id) = 1);
ALTER TABLE ring_group_members ADD UNIQUE (group_id, extension_id);
ALTER TABLE ring_group_members ADD UNIQUE (group_id, voice_agent_id);

ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
    CHECK (failure_kind IN ('none', 'voicemail', 'external', 'voice_agent'));

ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
    CHECK (destination_kind IN ('extension', 'external', 'sip_uri', 'voice_agent'));

-- The runtime singleton: what talking-agent last read and when (S-19, S-22).
CREATE TABLE voice_runtime (
    id                 BIGINT      PRIMARY KEY CHECK (id = 1),
    revision           BIGINT      NOT NULL DEFAULT 0 CHECK (revision >= 0),
    last_seen_at       TIMESTAMPTZ,
    loaded             JSONB,
    version            TEXT        NOT NULL DEFAULT '',
    service_account_id BIGINT      REFERENCES users (id) ON DELETE SET NULL
);

-- One global revision counter, bumped in the transaction of every persona,
-- attachment, server (credential included), enable or limit change, so the
-- runtime's ETag changes exactly when its view does (spec Data).
CREATE TABLE voice_revision (
    id       INT    PRIMARY KEY CHECK (id = 1),
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0)
);
INSERT INTO voice_revision (id, revision) VALUES (1, 0);

-- One row per call to an agent, written by reportVoiceCall and pruned by
-- retention hourly with an advisory lock, like the ai prune (spec Data).
CREATE TABLE voice_agent_calls (
    correlation_id TEXT        PRIMARY KEY,
    agent_name     TEXT        NOT NULL DEFAULT '',
    outcome        TEXT        NOT NULL DEFAULT 'unreported'
                   CHECK (outcome IN ('answered', 'unanswered', 'failed', 'unreported')),
    summary        TEXT        NOT NULL DEFAULT ''
                   CHECK (length(summary) <= 8000),
    tool_calls     JSONB,
    tokens_in      BIGINT      NOT NULL DEFAULT 0 CHECK (tokens_in >= 0),
    tokens_out     BIGINT      NOT NULL DEFAULT 0 CHECK (tokens_out >= 0),
    transcript     TEXT,
    reported_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE cdrs ADD COLUMN voice_agent_id   BIGINT REFERENCES voice_agents (id) ON DELETE SET NULL;
ALTER TABLE cdrs ADD COLUMN voice_agent_name TEXT NOT NULL DEFAULT '';
CREATE INDEX cdrs_voice_agent ON cdrs (voice_agent_name, start_time);

-- +goose Down
DROP INDEX cdrs_voice_agent;
ALTER TABLE cdrs DROP COLUMN voice_agent_name;
ALTER TABLE cdrs DROP COLUMN voice_agent_id;
DROP TABLE voice_agent_calls;
DROP TABLE voice_revision;
DROP TABLE voice_runtime;
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
    CHECK (destination_kind IN ('extension', 'external', 'sip_uri'));
ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
    CHECK (failure_kind IN ('none', 'voicemail', 'external'));
ALTER TABLE ring_group_members DROP CONSTRAINT ring_group_members_voice_agent_id_key;
ALTER TABLE ring_group_members DROP CONSTRAINT ring_group_members_group_id_extension_id_key;
ALTER TABLE ring_group_members DROP CONSTRAINT ring_group_members_target;
ALTER TABLE ring_group_members DROP COLUMN voice_agent_id;
ALTER TABLE ring_group_members ALTER COLUMN extension_id SET NOT NULL;
ALTER TABLE ring_group_members ADD PRIMARY KEY (group_id, extension_id);
DROP TABLE voice_agent_mcp;
DROP TABLE voice_mcp_servers;
DROP TABLE voice_agent_versions;
DROP TABLE voice_agents;
