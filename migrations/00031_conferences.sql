-- +goose Up
-- Server conference rooms (phone-feature-parity spec S-34): a room is dialled
-- by its number, optionally guarded by a user PIN and a leader PIN, mixed on
-- one hello-sip node and routable from a DID. Additive, so a rolling upgrade
-- works: an older node simply does not know the table.

CREATE TABLE conference_rooms (
    id                 BIGSERIAL   PRIMARY KEY,
    name               TEXT        NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    number             TEXT        NOT NULL UNIQUE CHECK (number ~ '^[0-9]{2,10}$'),
    -- PINs are sealed under HELLO_SECRET_KEY like trunk passwords and are
    -- write-only in the API. NULL means no PIN.
    user_pin_enc       BYTEA,
    leader_pin_enc     BYTEA,
    max_participants   INT         NOT NULL DEFAULT 10 CHECK (max_participants BETWEEN 2 AND 50),
    -- Participants wait (hearing the hold source) until a leader is in.
    wait_for_leader    BOOLEAN     NOT NULL DEFAULT FALSE,
    mute_on_entry      BOOLEAN     NOT NULL DEFAULT FALSE,
    -- What the room hears when someone joins or leaves.
    entry_exit         TEXT        NOT NULL DEFAULT 'tone' CHECK (entry_exit IN ('none', 'tone', 'announcement')),
    entry_announcement TEXT        NOT NULL DEFAULT '',
    exit_announcement  TEXT        NOT NULL DEFAULT '',
    record             BOOLEAN     NOT NULL DEFAULT FALSE,
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A DID can route to a room.
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
    CHECK (destination_kind IN ('extension', 'external', 'sip_uri', 'internal', 'conference'));

-- A participant's call is a conference call in the CDR.
ALTER TABLE cdrs DROP CONSTRAINT IF EXISTS cdrs_kind_check;
ALTER TABLE cdrs ADD CONSTRAINT cdrs_kind_check CHECK (kind IN ('call', 'page', 'intercom', 'conference'));

-- +goose Down
DELETE FROM inbound_routes WHERE destination_kind = 'conference';
UPDATE cdrs SET kind = 'call' WHERE kind = 'conference';
ALTER TABLE cdrs DROP CONSTRAINT cdrs_kind_check;
ALTER TABLE cdrs ADD CONSTRAINT cdrs_kind_check CHECK (kind IN ('call', 'page', 'intercom'));
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
    CHECK (destination_kind IN ('extension', 'external', 'sip_uri', 'internal'));
DROP TABLE conference_rooms;
