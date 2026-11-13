-- +goose Up
-- Locations, emergency calling and alarm calls (spec phone-feature-parity
-- S-28, S-29). A location is a site (the civic address and an optional geo
-- point), a floor of a site or a room of a floor. It can be assigned to a
-- phone group, an extension or a single phone; the most specific wins. The
-- HELD server and the Geolocation header of an emergency call read it. A
-- phone group also gets the E911 switch and an alarm destination for the
-- *55 alarm call, a trunk the geolocation flag, and one row of emergency
-- settings holds the ring group that is told of every emergency call. The
-- CDR kind alarm is new. Additive, so a rolling upgrade works: an older node
-- never reads these columns, and a deployment that assigns nothing behaves as
-- before.

CREATE TABLE locations (
    id           BIGSERIAL        PRIMARY KEY,
    kind         TEXT             NOT NULL CHECK (kind IN ('site', 'floor', 'room')),
    name         TEXT             NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- A site has no parent; a floor sits under a site, a room under a floor or
    -- a site (the API checks which). Deleting a location with children or
    -- assignments is refused.
    parent_id    BIGINT           REFERENCES locations (id) ON DELETE RESTRICT,
    -- The civic address (RFC 4119: country, A1, A3, A6, HNO, PC); a site only.
    country      TEXT             NOT NULL DEFAULT '' CHECK (country = '' OR country ~ '^[A-Z]{2}$'),
    state        TEXT             NOT NULL DEFAULT '' CHECK (char_length(state) <= 100),
    city         TEXT             NOT NULL DEFAULT '' CHECK (char_length(city) <= 100),
    street       TEXT             NOT NULL DEFAULT '' CHECK (char_length(street) <= 100),
    house_number TEXT             NOT NULL DEFAULT '' CHECK (char_length(house_number) <= 100),
    postal_code  TEXT             NOT NULL DEFAULT '' CHECK (char_length(postal_code) <= 100),
    -- An optional geo point (WGS 84) with its uncertainty in metres.
    latitude     DOUBLE PRECISION CHECK (latitude BETWEEN -90 AND 90),
    longitude    DOUBLE PRECISION CHECK (longitude BETWEEN -180 AND 180),
    radius_m     DOUBLE PRECISION CHECK (radius_m > 0 AND radius_m <= 1000000),
    created_at   TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK ((kind = 'site') = (parent_id IS NULL)),
    CHECK ((latitude IS NULL) = (longitude IS NULL)),
    CHECK (radius_m IS NULL OR latitude IS NOT NULL)
);
CREATE UNIQUE INDEX locations_name ON locations (COALESCE(parent_id, 0), lower(name));
CREATE INDEX locations_parent ON locations (parent_id);

ALTER TABLE phone_groups
    ADD COLUMN location_id  BIGINT  REFERENCES locations (id) ON DELETE RESTRICT,
    -- WP8x6 E911 and HELD provisioning is written only for a group with this on.
    ADD COLUMN e911_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    -- The extension or ring group number *55 rings; empty means no alarm.
    ADD COLUMN alarm_target TEXT    NOT NULL DEFAULT '' CHECK (alarm_target ~ '^[0-9A-Za-z._-]{0,64}$');
ALTER TABLE extensions ADD COLUMN location_id BIGINT REFERENCES locations (id) ON DELETE RESTRICT;
ALTER TABLE phones     ADD COLUMN location_id BIGINT REFERENCES locations (id) ON DELETE RESTRICT;
CREATE INDEX phone_groups_location ON phone_groups (location_id) WHERE location_id IS NOT NULL;
CREATE INDEX extensions_location   ON extensions (location_id)   WHERE location_id IS NOT NULL;
CREATE INDEX phones_location       ON phones (location_id)       WHERE location_id IS NOT NULL;

-- Emergency calls on a flagged trunk carry the caller's Geolocation header and
-- PIDF-LO body (RFC 6442).
ALTER TABLE trunks ADD COLUMN geolocation BOOLEAN NOT NULL DEFAULT FALSE;

-- One row. notify_group is the ring group (by number) every emergency call is
-- announced to, empty for none.
CREATE TABLE emergency_settings (
    id           SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    notify_group TEXT        NOT NULL DEFAULT '' CHECK (notify_group ~ '^[0-9A-Za-z._-]{0,64}$'),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO emergency_settings (id) VALUES (1);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pg_temp.hello_check_values(tbl regclass, cname text, add_vals text[], drop_vals text[])
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE
    def  text;
    col  text;
    vals text[];
BEGIN
    SELECT pg_get_constraintdef(c.oid) INTO def
      FROM pg_constraint c WHERE c.conrelid = tbl AND c.conname = cname;
    IF def IS NULL THEN
        RAISE EXCEPTION 'constraint % on % not found', cname, tbl;
    END IF;
    col := substring(def FROM '^CHECK \(+([a-z_]+) = ANY');
    IF col IS NULL THEN
        RAISE EXCEPTION 'constraint % on % has an unexpected form: %', cname, tbl, def;
    END IF;
    SELECT array_agg(m[1]) INTO vals FROM regexp_matches(def, '''([^'']*)''', 'g') AS t(m);
    SELECT array_agg(DISTINCT v ORDER BY v) INTO vals
      FROM unnest(vals || add_vals) AS u(v) WHERE v <> ALL (drop_vals);
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', tbl, cname);
    EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I CHECK (%I IN (%s))', tbl, cname, col,
        (SELECT string_agg(quote_literal(v), ',') FROM unnest(vals) AS u(v)));
END
$fn$;
-- +goose StatementEnd

SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY['alarm'], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pg_temp.hello_check_values(tbl regclass, cname text, add_vals text[], drop_vals text[])
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE
    def  text;
    col  text;
    vals text[];
BEGIN
    SELECT pg_get_constraintdef(c.oid) INTO def
      FROM pg_constraint c WHERE c.conrelid = tbl AND c.conname = cname;
    IF def IS NULL THEN
        RAISE EXCEPTION 'constraint % on % not found', cname, tbl;
    END IF;
    col := substring(def FROM '^CHECK \(+([a-z_]+) = ANY');
    IF col IS NULL THEN
        RAISE EXCEPTION 'constraint % on % has an unexpected form: %', cname, tbl, def;
    END IF;
    SELECT array_agg(m[1]) INTO vals FROM regexp_matches(def, '''([^'']*)''', 'g') AS t(m);
    SELECT array_agg(DISTINCT v ORDER BY v) INTO vals
      FROM unnest(vals || add_vals) AS u(v) WHERE v <> ALL (drop_vals);
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', tbl, cname);
    EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I CHECK (%I IN (%s))', tbl, cname, col,
        (SELECT string_agg(quote_literal(v), ',') FROM unnest(vals) AS u(v)));
END
$fn$;
-- +goose StatementEnd

UPDATE cdrs SET kind = 'call' WHERE kind = 'alarm';
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY[]::text[], ARRAY['alarm']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

DROP TABLE emergency_settings;
ALTER TABLE trunks DROP COLUMN geolocation;
ALTER TABLE phones DROP COLUMN location_id;
ALTER TABLE extensions DROP COLUMN location_id;
ALTER TABLE phone_groups DROP COLUMN alarm_target, DROP COLUMN e911_enabled, DROP COLUMN location_id;
DROP TABLE locations;
