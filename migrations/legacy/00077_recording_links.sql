-- +goose Up
-- Integrations P4 (spec integrations I-45): expiring, revocable links to call
-- recordings for people and systems that have no Hello session, such as a CRM
-- user. Only the SHA-256 of the 256-bit token is stored. A link dies with its
-- recording (ON DELETE CASCADE). max_uses 0 means unlimited. created_by is the
-- audit actor string of the minting credential; connector_id is reserved for
-- the CRM connectors (no table yet, so no foreign key). Additive, so a rolling
-- upgrade works.
CREATE TABLE recording_links (
    id            BIGSERIAL   PRIMARY KEY,
    token_hash    BYTEA       NOT NULL UNIQUE,
    recording_id  BIGINT      NOT NULL REFERENCES recordings (id) ON DELETE CASCADE,
    expires_at    TIMESTAMPTZ NOT NULL,
    max_uses      INTEGER     NOT NULL DEFAULT 0 CHECK (max_uses >= 0),
    uses          INTEGER     NOT NULL DEFAULT 0,
    last_used_at  TIMESTAMPTZ,
    created_by    TEXT        NOT NULL,
    connector_id  BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at    TIMESTAMPTZ
);
CREATE INDEX recording_links_recording ON recording_links (recording_id);
CREATE INDEX recording_links_expires ON recording_links (expires_at);

-- +goose Down
DROP TABLE recording_links;
