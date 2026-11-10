-- +goose Up
-- Web devices (clients-webrtc-mobile-video S-13, S-14, S-24): a user's
-- browser softphone is a device of their extension that has an owner, a
-- label and credentials derived on demand from a server key instead of a
-- stored digest secret.
--
-- A derived device stores no HA1: hello-sip computes it from
-- HELLO_CLIENT_AUTH_KEY and the presented username (w<id>.<expiryUnix>), so
-- the two HA1 columns become nullable. sip_username holds the stable base
-- name w<id>; the expiring name is only what the client presents.

ALTER TABLE devices
    ADD COLUMN kind        TEXT        NOT NULL DEFAULT 'desk' CHECK (kind IN ('desk', 'web', 'mobile')),
    ADD COLUMN user_id     BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    ADD COLUMN label       TEXT        NOT NULL DEFAULT '',
    ADD COLUMN platform    TEXT,
    ADD COLUMN app_version TEXT,
    ADD COLUMN last_seen_at TIMESTAMPTZ,
    ADD COLUMN auth_mode   TEXT        NOT NULL DEFAULT 'digest' CHECK (auth_mode IN ('digest', 'derived'));

ALTER TABLE devices
    ALTER COLUMN ha1_md5 DROP NOT NULL,
    ALTER COLUMN ha1_sha256 DROP NOT NULL;

ALTER TABLE devices
    ADD CONSTRAINT devices_ha1_present CHECK (auth_mode = 'derived' OR (ha1_md5 IS NOT NULL AND ha1_sha256 IS NOT NULL)),
    ADD CONSTRAINT devices_client_owner CHECK (kind = 'desk' OR user_id IS NOT NULL),
    ADD CONSTRAINT devices_client_derived CHECK (kind = 'desk' OR auth_mode = 'derived');

CREATE INDEX devices_user_id ON devices (user_id) WHERE user_id IS NOT NULL;

-- Which of a user's clients ring for a call to their extension.
ALTER TABLE users
    ADD COLUMN ring_web    BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN ring_mobile BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE users DROP COLUMN ring_mobile, DROP COLUMN ring_web;
DROP INDEX devices_user_id;
DELETE FROM devices WHERE kind <> 'desk';
ALTER TABLE devices
    DROP CONSTRAINT devices_client_derived,
    DROP CONSTRAINT devices_client_owner,
    DROP CONSTRAINT devices_ha1_present;
ALTER TABLE devices ALTER COLUMN ha1_sha256 SET NOT NULL, ALTER COLUMN ha1_md5 SET NOT NULL;
ALTER TABLE devices
    DROP COLUMN auth_mode, DROP COLUMN last_seen_at, DROP COLUMN app_version,
    DROP COLUMN platform, DROP COLUMN label, DROP COLUMN user_id, DROP COLUMN kind;
