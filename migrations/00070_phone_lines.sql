-- +goose Up
-- Multiple lines per phone and account sharing (spec phone-feature-parity
-- S-10 and S-11). A phone's line 1 stays phones.device_id; lines 2 and up are
-- further devices, one row each. A device is bound to at most one line of one
-- phone (the UNIQUE on device_id here, and the store refuses a device that is
-- line 1 of a phone), while one extension may appear on lines of several
-- phones: each phone binds a device of its own, which is a shared line
-- appearance. Additive, so a rolling upgrade works.
CREATE TABLE phone_lines (
    phone_id   BIGINT      NOT NULL REFERENCES phones (id) ON DELETE CASCADE,
    line       SMALLINT    NOT NULL CHECK (line BETWEEN 2 AND 6),
    device_id  BIGINT      NOT NULL UNIQUE REFERENCES devices (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (phone_id, line)
);

-- Account sharing (WP8x6 P22521 and P22522): a phone group switches it on,
-- and each of its phones is the host of the shared line or a guest on it.
ALTER TABLE phone_groups ADD COLUMN account_sharing BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE phones ADD COLUMN sharing_role TEXT NOT NULL DEFAULT 'host' CHECK (sharing_role IN ('host', 'guest'));

-- +goose Down
ALTER TABLE phones DROP COLUMN sharing_role;
ALTER TABLE phone_groups DROP COLUMN account_sharing;
DROP TABLE phone_lines;
