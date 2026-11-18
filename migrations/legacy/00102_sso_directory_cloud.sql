-- +goose Up
-- Google Workspace and Microsoft Entra directory connectors (integrations P9,
-- spec I-20 to I-22) and single sign-on (P11, I-26 to I-30): the two new
-- connector kinds, the OIDC providers, the identities SSO links to users,
-- the sso_required policy and the break-glass flag on local admins.
-- Additive, so a rolling upgrade works: an older node ignores the new tables
-- and columns and never sees a connector of a new kind it cannot run, since
-- only the new API creates one.

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

SELECT pg_temp.hello_check_values('directory_connectors', 'directory_connectors_kind_check', ARRAY['google', 'entra']::text[], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- The OIDC providers (I-26): the customer's own client, its secret sealed with
-- AAD sso:<provider>:client_secret, the Entra tenant (a GUID; common and
-- organizations are refused) or the Google Workspace domains (hd).
CREATE TABLE sso_providers (
    provider          TEXT        PRIMARY KEY CHECK (provider = ANY (ARRAY['google', 'entra'])),
    enabled           BOOLEAN     NOT NULL DEFAULT FALSE,
    client_id         TEXT        NOT NULL DEFAULT '' CHECK (char_length(client_id) <= 256),
    client_secret_enc BYTEA,
    tenant            TEXT        NOT NULL DEFAULT '' CHECK (char_length(tenant) <= 64),
    domains           TEXT[]      NOT NULL DEFAULT '{}',
    -- Just-in-time creation of a user on a first sign-in no link or synced
    -- user matches (off by default: synced users already exist).
    jit               BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- An identity at a provider linked to a user (I-28): the provider's stable
-- subject (Google sub, Entra oid) and tenant (hd or tid).
CREATE TABLE user_identities (
    id            BIGSERIAL   PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider      TEXT        NOT NULL CHECK (provider = ANY (ARRAY['google', 'entra'])),
    subject       TEXT        NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
    tenant        TEXT        NOT NULL DEFAULT '',
    email         TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX user_identities_subject ON user_identities (provider, subject);
CREATE UNIQUE INDEX user_identities_user ON user_identities (user_id, provider);

-- sso_required (I-29): only break-glass users may use the password form.
ALTER TABLE auth_settings ADD COLUMN sso_required BOOLEAN NOT NULL DEFAULT FALSE;

-- A local admin who may always sign in with a password; the store refuses to
-- remove the last enabled one (AC-10c).
ALTER TABLE users ADD COLUMN break_glass BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE users DROP COLUMN break_glass;
ALTER TABLE auth_settings DROP COLUMN sso_required;
DROP TABLE user_identities;
DROP TABLE sso_providers;
-- Connectors of the new kinds cannot be expressed by the older schema.
UPDATE users SET source_connector_id = NULL, external_id = NULL, managed_fields = '{}'
 WHERE source_connector_id IN (SELECT id FROM directory_connectors WHERE kind IN ('google', 'entra'));
UPDATE user_extensions SET source_connector_id = NULL
 WHERE source_connector_id IN (SELECT id FROM directory_connectors WHERE kind IN ('google', 'entra'));
UPDATE ring_group_members SET source_connector_id = NULL
 WHERE source_connector_id IN (SELECT id FROM directory_connectors WHERE kind IN ('google', 'entra'));
DELETE FROM directory_connectors WHERE kind IN ('google', 'entra');
ALTER TABLE directory_connectors DROP CONSTRAINT directory_connectors_kind_check;
ALTER TABLE directory_connectors ADD CONSTRAINT directory_connectors_kind_check CHECK (kind = ANY (ARRAY['ldap']));
