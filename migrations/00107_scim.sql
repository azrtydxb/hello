-- +goose Up
-- SCIM 2.0 server (integrations P10, spec I-23 to I-25): a directory
-- connector of kind scim that identity providers push users and groups to.
-- The pushed resources are kept as sent (so a GET answers what the IdP set),
-- and the directory engine maps them to Hello users exactly as it maps a
-- pulled source. Bearer tokens are stored as SHA-256 only. Additive, so a
-- rolling upgrade works: an older node ignores the new tables and columns.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pg_temp.hello_check_values(tbl regclass, cname text, add_vals text[], drop_vals text[])
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE
    def  text;
    col  text;
    vals text[];
BEGIN
    SELECT pg_get_constraintdef(c.oid) INTO def
      FROM pg_constraint c WHERE c.conrelid = tbl AND c.conname = cname;
    IF def IS NULL THEN
        RAISE EXCEPTION 'constraint % on % not found', cname, tbl;
    END IF;
    col := substring(def FROM '^CHECK \(+([a-z_]+) = ANY');
    IF col IS NULL THEN
        RAISE EXCEPTION 'constraint % on % has an unexpected form: %', cname, tbl, def;
    END IF;
    SELECT array_agg(m[1]) INTO vals FROM regexp_matches(def, '''([^'']*)''', 'g') AS t(m);
    SELECT array_agg(DISTINCT v ORDER BY v) INTO vals
      FROM unnest(vals || add_vals) AS u(v) WHERE v <> ALL (drop_vals);
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', tbl, cname);
    EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I CHECK (%I IN (%s))', tbl, cname, col,
        (SELECT string_agg(quote_literal(v), ',') FROM unnest(vals) AS u(v)));
END
$fn$;
-- +goose StatementEnd

SELECT pg_temp.hello_check_values('directory_connectors', 'directory_connectors_kind_check', ARRAY['scim']::text[], ARRAY[]::text[]);

-- The SCIM mass-deprovision guard (I-25): while paused_at is set the
-- connector refuses SCIM writes with 429 until an administrator confirms;
-- guard_since restarts the rolling hour after a confirmation, so the disables
-- that were confirmed are not counted again.
ALTER TABLE directory_connectors
    ADD COLUMN paused_at   TIMESTAMPTZ,
    ADD COLUMN guard_since TIMESTAMPTZ;

-- A pushed resource. id is Hello's SCIM id (a UUID); for a User it is also
-- users.external_id of the Hello user the engine links to it. name is the
-- userName of a User or the displayName of a Group; data holds the other
-- attributes as the IdP sent them (never a password). A DELETE of a User
-- sets deleted_at (the Hello user is disabled, never deleted); the row is
-- revived when the IdP creates the same userName or externalId again.
CREATE TABLE scim_resources (
    id            TEXT        PRIMARY KEY,
    connector_id  BIGINT      NOT NULL REFERENCES directory_connectors (id) ON DELETE CASCADE,
    resource_type TEXT        NOT NULL CHECK (resource_type IN ('User', 'Group')),
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 512),
    external_id   TEXT        CHECK (external_id IS NULL OR char_length(external_id) BETWEEN 1 AND 512),
    data          JSONB       NOT NULL DEFAULT '{}',
    version       BIGINT      NOT NULL DEFAULT 1,
    deleted_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX scim_resources_name ON scim_resources (connector_id, resource_type, lower(name)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX scim_resources_external ON scim_resources (connector_id, resource_type, external_id)
    WHERE external_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX scim_resources_list ON scim_resources (connector_id, resource_type, created_at, id);

-- Group membership, kept apart from the group's data: Entra and Okta add and
-- remove members one PATCH at a time and a group may have thousands.
CREATE TABLE scim_group_members (
    group_id   TEXT NOT NULL REFERENCES scim_resources (id) ON DELETE CASCADE,
    member_id  TEXT NOT NULL REFERENCES scim_resources (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, member_id)
);
CREATE INDEX scim_group_members_member ON scim_group_members (member_id);

-- SCIM bearer tokens: hello_scim_ + 256 random bits, shown once. Valid for
-- /scim/v2 of their connector only, with no API scope. At most two are
-- active per connector, so a token can be rotated without a gap.
CREATE TABLE scim_tokens (
    id            BIGSERIAL   PRIMARY KEY,
    connector_id  BIGINT      NOT NULL REFERENCES directory_connectors (id) ON DELETE CASCADE,
    token_hash    BYTEA       NOT NULL UNIQUE,
    -- The first characters after the prefix, so an administrator can tell
    -- tokens apart; not enough to use.
    hint          TEXT        NOT NULL DEFAULT '',
    created_by    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ
);
CREATE INDEX scim_tokens_connector ON scim_tokens (connector_id, id);

-- +goose Down
DROP TABLE scim_tokens;
DROP TABLE scim_group_members;
DROP TABLE scim_resources;
ALTER TABLE directory_connectors
    DROP COLUMN guard_since,
    DROP COLUMN paused_at;
