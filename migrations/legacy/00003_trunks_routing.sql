-- +goose Up
-- Phase 2: trunks, inbound/outbound routes, caller ID, CDR routing detail.
-- Owned by hello-control; hello-sip reads trunks and routes into its snapshot.

CREATE TABLE trunks (
    id                BIGSERIAL   PRIMARY KEY,
    name              TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    mode              TEXT        NOT NULL CHECK (mode IN ('registration', 'ip')),
    username          TEXT        NOT NULL DEFAULT '',
    -- AES-256-GCM under HELLO_SECRET_KEY: nonce || ciphertext; NULL when the
    -- trunk has no password.
    password_enc      BYTEA,
    realm             TEXT        NOT NULL DEFAULT '',
    from_domain       TEXT        NOT NULL DEFAULT '',
    register_expires  INTEGER     NOT NULL DEFAULT 3600 CHECK (register_expires BETWEEN 60 AND 86400),
    options_interval  INTEGER     NOT NULL DEFAULT 30 CHECK (options_interval BETWEEN 5 AND 3600),
    source_cidrs      CIDR[]      NOT NULL DEFAULT '{}',
    max_calls         INTEGER     NOT NULL DEFAULT 0 CHECK (max_calls >= 0), -- 0 = unlimited
    default_caller_id TEXT        NOT NULL DEFAULT '',
    enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE trunk_destinations (
    id       BIGSERIAL PRIMARY KEY,
    trunk_id BIGINT    NOT NULL REFERENCES trunks (id) ON DELETE CASCADE,
    host     TEXT      NOT NULL,
    port     INTEGER   NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535), -- 0 = SRV, else 5060
    priority INTEGER   NOT NULL DEFAULT 0,
    weight   INTEGER   NOT NULL DEFAULT 1 CHECK (weight > 0)
);
CREATE INDEX trunk_destinations_trunk ON trunk_destinations (trunk_id);

CREATE TABLE outbound_routes (
    id                 BIGSERIAL PRIMARY KEY,
    position           INTEGER   NOT NULL,
    name               TEXT      NOT NULL,
    match_kind         TEXT      NOT NULL CHECK (match_kind IN ('prefix', 'regex')),
    match              TEXT      NOT NULL CHECK (length(match) <= 500),
    source_extensions  TEXT[]    NOT NULL DEFAULT '{}',
    schedule           JSONB,    -- routing.Schedule; NULL = always
    number_transform   JSONB     NOT NULL DEFAULT '{}', -- routing.Transform
    callerid_transform JSONB     NOT NULL DEFAULT '{}', -- routing.Transform
    failover_codes     INTEGER[] NOT NULL DEFAULT '{408,480,500,502,503,504}',
    emergency          BOOLEAN   NOT NULL DEFAULT FALSE,
    enabled            BOOLEAN   NOT NULL DEFAULT TRUE
);
-- Deferred so a reorder can swap positions inside one transaction.
ALTER TABLE outbound_routes ADD CONSTRAINT outbound_routes_position UNIQUE (position) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE outbound_route_trunks (
    route_id BIGINT  NOT NULL REFERENCES outbound_routes (id) ON DELETE CASCADE,
    trunk_id BIGINT  NOT NULL REFERENCES trunks (id) ON DELETE RESTRICT,
    position INTEGER NOT NULL,
    PRIMARY KEY (route_id, position),
    UNIQUE (route_id, trunk_id)
);

CREATE TABLE inbound_routes (
    id                 BIGSERIAL PRIMARY KEY,
    position           INTEGER   NOT NULL,
    name               TEXT      NOT NULL,
    did_kind           TEXT      NOT NULL CHECK (did_kind IN ('any', 'exact', 'prefix', 'regex')),
    did                TEXT      NOT NULL DEFAULT '' CHECK (length(did) <= 500),
    trunk_id           BIGINT    REFERENCES trunks (id) ON DELETE CASCADE, -- NULL = any trunk
    sip_domain         TEXT      NOT NULL DEFAULT '',
    header_name        TEXT      NOT NULL DEFAULT '',
    header_regex       TEXT      NOT NULL DEFAULT '' CHECK (length(header_regex) <= 500),
    schedule           JSONB,
    callerid_transform JSONB     NOT NULL DEFAULT '{}',
    destination_kind   TEXT      NOT NULL CHECK (destination_kind IN ('extension', 'external', 'sip_uri')),
    destination        TEXT      NOT NULL,
    enabled            BOOLEAN   NOT NULL DEFAULT TRUE
);
-- Deferred so a reorder can swap positions inside one transaction.
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_position UNIQUE (position) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE extensions ADD COLUMN external_number TEXT NOT NULL DEFAULT ''
    CHECK (external_number ~ '^(\+?[0-9]{2,20})?$');

ALTER TABLE cdrs
    ADD COLUMN direction             TEXT  NOT NULL DEFAULT 'internal' CHECK (direction IN ('internal', 'inbound', 'outbound')),
    ADD COLUMN original_destination  TEXT  NOT NULL DEFAULT '',
    ADD COLUMN rewritten_destination TEXT  NOT NULL DEFAULT '',
    ADD COLUMN route_name            TEXT  NOT NULL DEFAULT '',
    ADD COLUMN trunk_name            TEXT  NOT NULL DEFAULT '',
    ADD COLUMN trace                 JSONB NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE cdrs
    DROP COLUMN trace, DROP COLUMN trunk_name, DROP COLUMN route_name,
    DROP COLUMN rewritten_destination, DROP COLUMN original_destination, DROP COLUMN direction;
ALTER TABLE extensions DROP COLUMN external_number;
DROP TABLE inbound_routes;
DROP TABLE outbound_route_trunks;
DROP TABLE outbound_routes;
DROP TABLE trunk_destinations;
DROP TABLE trunks;
