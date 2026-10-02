-- +goose Up
-- Configuration revision counter; later phases bump it on every validated
-- configuration change (spec §30: validate, revision, and audit).
CREATE TABLE schema_info (
    id                  BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    config_revision     BIGINT      NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO schema_info DEFAULT VALUES;

-- +goose Down
DROP TABLE schema_info;
