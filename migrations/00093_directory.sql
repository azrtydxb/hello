-- +goose Up
-- Directory engine and AD/LDAP sync (integrations P8, spec I-1 to I-19):
-- connectors, sync runs and their changes, and the ownership columns that
-- tie a user, an extension link and a ring-group member to the connector that
-- manages it. Additive, so a rolling upgrade works: an older node ignores the
-- new tables and columns.

CREATE TABLE directory_connectors (
    id                   BIGSERIAL   PRIMARY KEY,
    -- Written as = ANY (ARRAY[...]) so that later connector kinds (Google, Entra,
    -- SCIM) can be added with pg_temp.hello_check_values, which a one-value IN list
    -- (stored as a plain equality) would defeat.
    kind                 TEXT        NOT NULL CHECK (kind = ANY (ARRAY['ldap'])),
    name                 TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    enabled              BOOLEAN     NOT NULL DEFAULT FALSE,
    mode                 TEXT        NOT NULL DEFAULT 'authoritative' CHECK (mode IN ('authoritative', 'create_only', 'link_only')),
    schedule_minutes     INT         NOT NULL DEFAULT 60 CHECK (schedule_minutes = 0 OR schedule_minutes BETWEEN 5 AND 10080),
    config               JSONB       NOT NULL DEFAULT '{}',
    mappings             JSONB       NOT NULL DEFAULT '{}',
    group_rules          JSONB       NOT NULL DEFAULT '{}',
    options              JSONB       NOT NULL DEFAULT '{}',
    -- Reserved for range auto-provisioning (I-11); stored, not acted on.
    auto_provision       JSONB       NOT NULL DEFAULT '{}',
    -- The sealed secret (the LDAP bind password), AAD directory:<id>:credential.
    credential_enc       BYTEA,
    -- A short fingerprint of what the secret is bound to (host and bind DN),
    -- so a change of either demands the secret again (T-4).
    credential_fp        TEXT        NOT NULL DEFAULT '',
    -- Incremental runs (I-12): the high-water mark of the last applied read
    -- ({} none), a fingerprint of the config, mappings, rules and mode it was
    -- taken under, and when the last full run finished (full runs are the only
    -- ones that detect absences, so one is forced every 24 hours).
    sync_mark            JSONB       NOT NULL DEFAULT '{}',
    sync_fingerprint     TEXT        NOT NULL DEFAULT '',
    last_full_run_at     TIMESTAMPTZ,
    status               TEXT        NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'ok', 'failing')),
    last_run_at          TIMESTAMPTZ,
    last_success_at      TIMESTAMPTZ,
    last_error_code      TEXT        NOT NULL DEFAULT '',
    last_error_detail    TEXT        NOT NULL DEFAULT '',
    consecutive_failures INT         NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX directory_connectors_name ON directory_connectors (lower(name));

CREATE TABLE directory_sync_runs (
    id            BIGSERIAL   PRIMARY KEY,
    connector_id  BIGINT      NOT NULL REFERENCES directory_connectors (id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('preview', 'apply', 'scheduled', 'scim')),
    status        TEXT        NOT NULL CHECK (status IN ('queued', 'running', 'preview', 'succeeded', 'failed', 'needs_confirmation', 'stale')),
    requested_by  TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    replica       TEXT        NOT NULL DEFAULT '',
    counts        JSONB       NOT NULL DEFAULT '{}',
    diff_hash     TEXT        NOT NULL DEFAULT '',
    delta_token   TEXT        NOT NULL DEFAULT '',
    -- Whether the run read only the changes since the last run, and why a full
    -- run was full (manual, no_mark, full_interval, config_changed, ...).
    incremental   BOOLEAN     NOT NULL DEFAULT FALSE,
    full_reason   TEXT        NOT NULL DEFAULT '',
    error_code    TEXT        NOT NULL DEFAULT '',
    error_detail  TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX directory_sync_runs_connector ON directory_sync_runs (connector_id, id DESC);
CREATE INDEX directory_sync_runs_open ON directory_sync_runs (connector_id, status) WHERE status IN ('queued', 'running');

CREATE TABLE directory_sync_changes (
    id           BIGSERIAL   PRIMARY KEY,
    run_id       BIGINT      NOT NULL REFERENCES directory_sync_runs (id) ON DELETE CASCADE,
    action       TEXT        NOT NULL CHECK (action IN ('create', 'update', 'link', 'adopt', 'disable', 'enable', 'conflict', 'skip', 'error')),
    user_id      BIGINT,
    external_id  TEXT        NOT NULL DEFAULT '',
    username     TEXT        NOT NULL DEFAULT '',
    field_diffs  JSONB       NOT NULL DEFAULT '{}',
    reason       TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX directory_sync_changes_run ON directory_sync_changes (run_id, id);
CREATE INDEX directory_sync_changes_age ON directory_sync_changes (created_at);

-- A user a directory manages: the connector, the stable external id, the
-- fields it owns (read-only for administrators), when sync disabled it, and
-- how many complete runs it has been absent from.
ALTER TABLE users
    ADD COLUMN source_connector_id BIGINT REFERENCES directory_connectors (id),
    ADD COLUMN external_id         TEXT,
    ADD COLUMN managed_fields      TEXT[]      NOT NULL DEFAULT '{}',
    ADD COLUMN disabled_at         TIMESTAMPTZ,
    ADD COLUMN absent_runs         INT         NOT NULL DEFAULT 0,
    ADD CONSTRAINT users_source_pair CHECK ((source_connector_id IS NULL) = (external_id IS NULL));
CREATE UNIQUE INDEX users_source_external ON users (source_connector_id, external_id) WHERE source_connector_id IS NOT NULL;

-- The extension link and the ring-group rows sync owns; the manual ones have
-- no connector and sync never touches them (I-4, I-10).
ALTER TABLE user_extensions ADD COLUMN source_connector_id BIGINT REFERENCES directory_connectors (id);
ALTER TABLE ring_group_members ADD COLUMN source_connector_id BIGINT REFERENCES directory_connectors (id);
-- A device sync switched off, so only those come back when the user does.
ALTER TABLE devices ADD COLUMN disabled_by_directory BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE devices DROP COLUMN disabled_by_directory;
ALTER TABLE ring_group_members DROP COLUMN source_connector_id;
ALTER TABLE user_extensions DROP COLUMN source_connector_id;
DROP INDEX users_source_external;
ALTER TABLE users
    DROP CONSTRAINT users_source_pair,
    DROP COLUMN absent_runs,
    DROP COLUMN disabled_at,
    DROP COLUMN managed_fields,
    DROP COLUMN external_id,
    DROP COLUMN source_connector_id;
DROP TABLE directory_sync_changes;
DROP TABLE directory_sync_runs;
DROP TABLE directory_connectors;
