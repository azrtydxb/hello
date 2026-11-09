-- +goose Up
-- Dial-by-name directory (pbx-gaps S-13, decision D11). A caller spells a name
-- on the keypad and is connected to the extension. "directory" is a new
-- destination kind with no target, accepted by every output that stores a
-- call destination (inbound routes, ring group fallback, IVR keys, timeout and
-- invalid choice, time condition branches, queue exits) and a new feature-code
-- action. The lists are read from the live constraint, so values other
-- migrations added (applied in either order) are kept. Additive: an older
-- hello-sip never sees a row of the new kind until the API accepts one.
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

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ring_groups', 'ring_groups_failure_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_open_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_closed_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_holiday_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_timeout_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_full_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_no_agents_kind_check', ARRAY['directory']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY['directory']::text[], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- The language of the spoken prompts a menu's applications add (pbx-gaps S-20,
-- the same resolution as a voicemail box: NULL inherits the deployment language).
ALTER TABLE ivr_menus ADD COLUMN prompt_language TEXT;

-- Existing installs get the default code *42 when it is free (no other
-- feature code or page code equal to, a prefix of, or prefixed by it); a
-- fresh install (empty table) gets it from the control process's defaults
-- instead, so the "seed only an empty table" rule still holds.
INSERT INTO feature_codes (code, action, argument)
SELECT v.code, v.action, v.argument
FROM (VALUES ('*42', 'directory', '')) AS v(code, action, argument)
WHERE EXISTS (SELECT 1 FROM feature_codes)
  AND NOT EXISTS (SELECT 1 FROM feature_codes f WHERE starts_with(f.code, v.code) OR starts_with(v.code, f.code))
  AND NOT EXISTS (SELECT 1 FROM paging_groups p WHERE starts_with(p.code, v.code) OR starts_with(v.code, p.code));

-- +goose Down
ALTER TABLE ivr_menus DROP COLUMN prompt_language;
-- A destination of the removed kind cannot be expressed by an older version:
-- routes and keys using it are removed, the other outputs fall back to a hangup.
DELETE FROM inbound_routes WHERE destination_kind = 'directory';
UPDATE ring_groups SET failure_kind = 'none', failure_target = '' WHERE failure_kind = 'directory';
UPDATE ivr_menus SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'directory';
UPDATE ivr_menus SET invalid_kind = 'hangup', invalid_target = '' WHERE invalid_kind = 'directory';
DELETE FROM ivr_menu_options WHERE dest_kind = 'directory';
UPDATE time_conditions SET open_kind = 'hangup', open_target = '', open_then = '' WHERE open_kind = 'directory';
UPDATE time_conditions SET closed_kind = 'hangup', closed_target = '', closed_then = '' WHERE closed_kind = 'directory';
UPDATE time_conditions SET holiday_kind = '', holiday_target = '', holiday_then = '' WHERE holiday_kind = 'directory';
UPDATE queues SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'directory';
UPDATE queues SET full_kind = 'hangup', full_target = '' WHERE full_kind = 'directory';
UPDATE queues SET no_agents_kind = 'hangup', no_agents_target = '' WHERE no_agents_kind = 'directory';
DELETE FROM feature_codes WHERE action = 'directory';
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
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('ring_groups', 'ring_groups_failure_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_open_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_closed_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_holiday_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_timeout_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_full_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_no_agents_kind_check', ARRAY[]::text[], ARRAY['directory']::text[]);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY[]::text[], ARRAY['directory']::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);
