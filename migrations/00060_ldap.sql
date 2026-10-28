-- +goose Up
-- Phone LDAP directory (spec pbx-gaps S-24 to S-26). A phone group turns the
-- directory on; each phone then has its own bind DN and a generated password,
-- sealed under HELLO_SECRET_KEY (the rendered configuration carries it, so it
-- must be recoverable). ldap_prev_password_enc keeps the old password valid
-- after a rotation until the phone has fetched its new configuration.
-- Additive: a group with ldap_enabled false provisions as before.
ALTER TABLE phone_groups
    ADD COLUMN ldap_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE phones
    ADD COLUMN ldap_dn                 TEXT,
    ADD COLUMN ldap_password_enc       BYTEA,
    ADD COLUMN ldap_prev_password_enc  BYTEA,
    ADD COLUMN ldap_rotated_at         TIMESTAMPTZ,
    ADD COLUMN ldap_last_bind_at       TIMESTAMPTZ;

-- +goose Down
ALTER TABLE phones
    DROP COLUMN ldap_last_bind_at,
    DROP COLUMN ldap_rotated_at,
    DROP COLUMN ldap_prev_password_enc,
    DROP COLUMN ldap_password_enc,
    DROP COLUMN ldap_dn;
ALTER TABLE phone_groups DROP COLUMN ldap_enabled;
