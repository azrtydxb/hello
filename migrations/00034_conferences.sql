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

-- A DID can route to a room, and a participant's call is a conference call
-- in the CDR. Each CHECK list is read from the live constraint, so values
-- other migrations added (applied in either order) are kept; the helper is
-- dropped again so a later migration on the same session can define its own.
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

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY['conference'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY['conference'], ARRAY[]::text[]);
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

DELETE FROM inbound_routes WHERE destination_kind = 'conference';
UPDATE cdrs SET kind = 'call' WHERE kind = 'conference';
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY[]::text[], ARRAY['conference']);
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY[]::text[], ARRAY['conference']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);
DROP TABLE conference_rooms;
