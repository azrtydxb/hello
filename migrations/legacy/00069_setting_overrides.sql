-- +goose Up
-- Setting overrides (spec phone-feature-parity S-12): extra vendor keys per
-- phone group and per phone. The render order is the template, then the
-- group, then the phone. A secret value is sealed (value_enc, with the scope,
-- owner and key as additional data) and value stays empty. Additive: an older
-- hello-control ignores the table.
CREATE TABLE prov_setting_overrides (
    id         BIGSERIAL   PRIMARY KEY,
    group_id   BIGINT      REFERENCES phone_groups (id) ON DELETE CASCADE,
    phone_id   BIGINT      REFERENCES phones (id) ON DELETE CASCADE,
    key        TEXT        NOT NULL CHECK (key ~ '^[A-Za-z0-9_.]{1,128}$'),
    value      TEXT        NOT NULL DEFAULT '' CHECK (length(value) <= 1024),
    value_enc  BYTEA,
    secret     BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((group_id IS NULL) <> (phone_id IS NULL)),
    CHECK (secret = (value_enc IS NOT NULL)),
    CHECK (NOT secret OR value = '')
);
CREATE UNIQUE INDEX prov_setting_overrides_group_key ON prov_setting_overrides (group_id, key) WHERE group_id IS NOT NULL;
CREATE UNIQUE INDEX prov_setting_overrides_phone_key ON prov_setting_overrides (phone_id, key) WHERE phone_id IS NOT NULL;

-- +goose Down
DROP TABLE prov_setting_overrides;
