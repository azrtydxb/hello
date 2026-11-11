-- +goose Up
-- Per-group account sharing password (WP8x6 P22523, "Group Password").
-- Hello generates the secret when a phone group is created (or, for a group
-- that predates this, when account sharing is first switched on or a phone
-- renders), seals it with the secret box and writes it only into the
-- provisioned configuration of the group's WP8x6 phones. It is never returned
-- by the API. NULL means no secret yet. Additive, so a rolling upgrade works.
ALTER TABLE phone_groups ADD COLUMN account_sharing_secret_enc BYTEA;

-- +goose Down
ALTER TABLE phone_groups DROP COLUMN account_sharing_secret_enc;
