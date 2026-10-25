-- +goose Up
-- Check-sync pushes (phone-feature-parity S-5): a request to resync or
-- reboot a phone is a row. hello-control inserts it and NOTIFYs
-- hello_checksync; one hello-sip node claims it (status pending -> sending,
-- at most HELLO_PROV_PUSH_RATE claims per minute cluster-wide), sends the
-- NOTIFY to the phone's registered contacts and writes the result.
-- Additive, so a rolling upgrade works.

CREATE TABLE phone_pushes (
    id           BIGSERIAL   PRIMARY KEY,
    phone_id     BIGINT      NOT NULL REFERENCES phones (id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL CHECK (kind IN ('resync', 'reboot')),
    source       TEXT        NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'auto')),
    actor        TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'sending', 'ok', 'not_registered', 'timeout', 'auth_failed', 'failed')),
    detail       TEXT        NOT NULL DEFAULT '',
    contacts     INT         NOT NULL DEFAULT 0,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at   TIMESTAMPTZ,
    claimed_by   TEXT        NOT NULL DEFAULT '',
    finished_at  TIMESTAMPTZ
);
CREATE INDEX phone_pushes_phone ON phone_pushes (phone_id, id DESC);
CREATE INDEX phone_pushes_open ON phone_pushes (status, id) WHERE status IN ('pending', 'sending');
CREATE INDEX phone_pushes_claimed ON phone_pushes (claimed_at) WHERE claimed_at IS NOT NULL;

-- The hash of what the phone's configuration last rendered to, for the
-- automatic resync after a change (HELLO_PROV_AUTO_RESYNC).
ALTER TABLE phones ADD COLUMN sync_hash TEXT;

-- +goose Down
ALTER TABLE phones DROP COLUMN sync_hash;
DROP TABLE phone_pushes;
