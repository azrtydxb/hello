-- +goose Up
-- Integrations foundation (spec integrations P0): first-party OAuth clients
-- and the extensions a service account may act for.

-- A pre-registered client is one of Hello's own apps (the dialer, the
-- browser extension, the CRM iframes): a public PKCE client with fixed
-- redirect URIs, registered by the phase that ships the app.
ALTER TABLE oauth_clients DROP CONSTRAINT oauth_clients_kind_check;
ALTER TABLE oauth_clients ADD CONSTRAINT oauth_clients_kind_check
    CHECK (kind IN ('cimd', 'dcr', 'service', 'preregistered'));

-- The extension numbers a service account may originate calls for or
-- receive events about. NULL is every extension (set by an administrator).
ALTER TABLE oauth_clients ADD COLUMN extension_scope TEXT[];

-- +goose Down
ALTER TABLE oauth_clients DROP COLUMN extension_scope;
ALTER TABLE oauth_clients DROP CONSTRAINT oauth_clients_kind_check;
ALTER TABLE oauth_clients ADD CONSTRAINT oauth_clients_kind_check
    CHECK (kind IN ('cimd', 'dcr', 'service'));
