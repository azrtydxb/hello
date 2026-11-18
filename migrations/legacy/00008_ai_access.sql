-- +goose Up
-- External AI access (spec ai-external-access): user roles, scoped and
-- expiring API tokens, and the OAuth 2.1 authorization server's clients,
-- secrets, pending requests, grants and tokens. Only SHA-256 hashes of
-- credentials are stored.

-- Roles (S-23). Existing users keep everything they could do: admin.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'viewer'
    CHECK (role IN ('viewer', 'operator', 'admin'));
UPDATE users SET role = 'admin';

-- Personal tokens (S-6). NULL scopes is a legacy token with every scope.
ALTER TABLE api_tokens ADD COLUMN scopes TEXT[];
ALTER TABLE api_tokens ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE api_tokens ADD COLUMN revoked_at TIMESTAMPTZ;
ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'legacy'
    CHECK (kind IN ('legacy', 'personal'));

-- How a change was made: NULL for the console and API tokens, the OAuth
-- client for changes made through an access token (S-6).
ALTER TABLE audit_events ADD COLUMN via TEXT;

-- Clients: client ID metadata documents (cimd), dynamically registered
-- public clients (dcr) and service accounts (service, S-11).
CREATE TABLE oauth_clients (
    client_id     TEXT        PRIMARY KEY,
    kind          TEXT        NOT NULL CHECK (kind IN ('cimd', 'dcr', 'service')),
    name          TEXT        NOT NULL,
    client_uri    TEXT,
    redirect_uris TEXT[]      NOT NULL DEFAULT '{}',
    metadata      JSONB,
    fetched_at    TIMESTAMPTZ,
    cache_until   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    -- Service accounts only.
    description   TEXT,
    role          TEXT        CHECK (role IN ('viewer', 'operator', 'admin')),
    scopes        TEXT[],
    enabled       BOOLEAN     NOT NULL DEFAULT true,
    created_by    BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    CHECK (kind <> 'service' OR (role IS NOT NULL AND scopes IS NOT NULL))
);

-- Service-account client secrets; at most two unrevoked per client, checked
-- in the creating transaction.
CREATE TABLE oauth_client_secrets (
    id           BIGSERIAL   PRIMARY KEY,
    client_id    TEXT        NOT NULL REFERENCES oauth_clients (client_id) ON DELETE CASCADE,
    secret_hash  BYTEA       NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);
CREATE INDEX oauth_client_secrets_client ON oauth_client_secrets (client_id);

-- Authorization requests awaiting consent, then their single-use code.
CREATE TABLE oauth_requests (
    id              TEXT        PRIMARY KEY,
    client_id       TEXT        NOT NULL REFERENCES oauth_clients (client_id) ON DELETE CASCADE,
    redirect_uri    TEXT        NOT NULL,
    state           TEXT,
    code_challenge  TEXT        NOT NULL,
    scopes          TEXT[]      NOT NULL,
    resources       TEXT[]      NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    user_id         BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    code_hash       BYTEA       UNIQUE,
    code_expires_at TIMESTAMPTZ,
    code_used_at    TIMESTAMPTZ
);
CREATE INDEX oauth_requests_expires ON oauth_requests (expires_at);

-- A user's consent to a client: the scopes and resources it may hold.
CREATE TABLE oauth_grants (
    id           BIGSERIAL   PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    client_id    TEXT        NOT NULL REFERENCES oauth_clients (client_id) ON DELETE CASCADE,
    scopes       TEXT[]      NOT NULL,
    resources    TEXT[]      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX oauth_grants_user ON oauth_grants (user_id);

-- Access and refresh tokens. grant_id is NULL for client credentials.
CREATE TABLE oauth_tokens (
    hash       BYTEA       PRIMARY KEY,
    kind       TEXT        NOT NULL CHECK (kind IN ('access', 'refresh')),
    grant_id   BIGINT      REFERENCES oauth_grants (id) ON DELETE CASCADE,
    client_id  TEXT        NOT NULL REFERENCES oauth_clients (client_id) ON DELETE CASCADE,
    user_id    BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    scopes     TEXT[]      NOT NULL,
    resources  TEXT[]      NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    spent_at   TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX oauth_tokens_grant ON oauth_tokens (grant_id);
CREATE INDEX oauth_tokens_expires ON oauth_tokens (expires_at);

-- +goose Down
DROP TABLE oauth_tokens;
DROP TABLE oauth_grants;
DROP TABLE oauth_requests;
DROP TABLE oauth_client_secrets;
DROP TABLE oauth_clients;
ALTER TABLE audit_events DROP COLUMN via;
ALTER TABLE api_tokens DROP COLUMN kind;
ALTER TABLE api_tokens DROP COLUMN revoked_at;
ALTER TABLE api_tokens DROP COLUMN expires_at;
ALTER TABLE api_tokens DROP COLUMN scopes;
ALTER TABLE users DROP COLUMN role;
