-- +goose Up
-- IVR / auto-attendant. A menu plays a greeting (an announcement), collects
-- DTMF digits and routes the call to the destination of the chosen option.
-- Prompts are announcements (migration 00005), so any uploaded or generated
-- announcement works. Additive, so a rolling upgrade works.

CREATE TABLE ivr_menus (
    id                  BIGSERIAL   PRIMARY KEY,
    name                TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    -- The number that dials the menu directly; empty means it is only
    -- reachable as a destination (inbound route, ring group failover, a
    -- feature code, another menu's option).
    number              TEXT        NOT NULL DEFAULT '' CHECK (number ~ '^([0-9]{2,16})?$'),
    enabled             BOOLEAN     NOT NULL DEFAULT TRUE,
    greeting_id         BIGINT      NOT NULL REFERENCES announcements (id) ON DELETE RESTRICT,
    invalid_prompt_id   BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    timeout_prompt_id   BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    -- Seconds to wait for the first digit once the greeting has played, and
    -- between the digits of a direct-dialled extension.
    digit_timeout       INTEGER     NOT NULL DEFAULT 5 CHECK (digit_timeout BETWEEN 1 AND 60),
    inter_digit_timeout INTEGER     NOT NULL DEFAULT 3 CHECK (inter_digit_timeout BETWEEN 1 AND 30),
    -- How often the menu repeats after a timeout or an invalid choice before
    -- the matching exit destination applies.
    max_retries         INTEGER     NOT NULL DEFAULT 3 CHECK (max_retries BETWEEN 0 AND 10),
    -- Whether the caller may dial an extension number from the menu.
    allow_direct_dial   BOOLEAN     NOT NULL DEFAULT FALSE,
    timeout_kind        TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (timeout_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external')),
    timeout_target      TEXT        NOT NULL DEFAULT '',
    invalid_kind        TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (invalid_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external')),
    invalid_target      TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ivr_menus_number ON ivr_menus (number) WHERE number <> '';

-- One row per option. Destination kinds: hangup, extension (target: the
-- number), ring_group (the group name), voicemail (the extension number of
-- the box), announcement (the name), paging (the page code), ivr (a menu
-- name) and external (a number routed through outbound routes).
CREATE TABLE ivr_menu_options (
    menu_id     BIGINT NOT NULL REFERENCES ivr_menus (id) ON DELETE CASCADE,
    digit       TEXT   NOT NULL CHECK (digit ~ '^[0-9*#]$'),
    dest_kind   TEXT   NOT NULL
        CHECK (dest_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external')),
    dest_target TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (menu_id, digit)
);

-- Widen the CHECK lists. Each list is read from the live constraint, so
-- values other migrations added (applied in either order) are kept. The
-- helper is dropped again so a later migration on the same session can
-- define its own.
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

SELECT pg_temp.hello_check_values('ring_groups', 'ring_groups_failure_kind_check', ARRAY['ivr'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY['ivr'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY['ivr'], ARRAY[]::text[]);
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

DELETE FROM inbound_routes WHERE destination_kind = 'ivr';
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY[]::text[], ARRAY['ivr']);
DELETE FROM feature_codes WHERE action = 'ivr';
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY[]::text[], ARRAY['ivr']);
UPDATE ring_groups SET failure_kind = 'none', failure_target = '' WHERE failure_kind = 'ivr';
SELECT pg_temp.hello_check_values('ring_groups', 'ring_groups_failure_kind_check', ARRAY[]::text[], ARRAY['ivr']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

DROP TABLE ivr_menu_options;
DROP TABLE ivr_menus;
