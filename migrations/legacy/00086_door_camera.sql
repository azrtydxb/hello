-- +goose Up
-- Door station camera (spec phone-feature-parity S-49): the address of a door
-- station's picture, which hello-control fetches and passes to the console so
-- the user who answers can see who is at the door. kind is mjpeg (a
-- multipart/x-mixed-replace stream) or snapshot (one JPEG per request). The
-- password is sealed under HELLO_SECRET_KEY (AAD binds it to the extension)
-- and never returned. Additive: an older node ignores the table.
CREATE TABLE door_cameras (
    extension_id  BIGINT      PRIMARY KEY REFERENCES extensions (id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('mjpeg', 'snapshot')),
    url           TEXT        NOT NULL CHECK (length(url) BETWEEN 8 AND 2048),
    username      TEXT        NOT NULL DEFAULT '' CHECK (length(username) <= 128),
    password_enc  BYTEA,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE door_cameras;
