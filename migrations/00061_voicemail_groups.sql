-- +goose Up
-- Voicemail groups (spec pbx-gaps S-14): a group has a dial number and
-- member extensions that have a voicemail box. A message left for the group
-- (an IVR or route destination, a dial of the number) or broadcast by a
-- member with *63<number> is delivered as one stored object and one
-- voicemail_messages row per member box. The destination kind
-- voicemail_group and the feature-code action voicemail_broadcast are added
-- to the live CHECK lists, so values other migrations added are kept.
-- Additive, so a rolling upgrade works: an older hello-sip never sees a row
-- of a new kind until the API accepts one.
CREATE TABLE voicemail_groups (
    id          BIGSERIAL   PRIMARY KEY,
    number      TEXT        NOT NULL UNIQUE CHECK (number ~ '^[0-9]{2,16}$'),
    name        TEXT        NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    max_seconds INTEGER     NOT NULL DEFAULT 180 CHECK (max_seconds BETWEEN 10 AND 600),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE voicemail_group_members (
    group_id     BIGINT NOT NULL REFERENCES voicemail_groups (id) ON DELETE CASCADE,
    extension_id BIGINT NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, extension_id)
);
CREATE INDEX voicemail_group_members_extension ON voicemail_group_members (extension_id);

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

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_open_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_closed_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_holiday_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_timeout_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_full_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_no_agents_kind_check', ARRAY['voicemail_group']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY['voicemail_broadcast']::text[], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- Existing installs get the default broadcast code when it is free (no other
-- feature code or page code equal to, a prefix of, or prefixed by it); a
-- fresh install (empty table) gets it from the control process's defaults.
INSERT INTO feature_codes (code, action, argument)
SELECT v.code, v.action, v.argument
FROM (VALUES ('*63', 'voicemail_broadcast', '')) AS v(code, action, argument)
WHERE EXISTS (SELECT 1 FROM feature_codes)
  AND NOT EXISTS (SELECT 1 FROM feature_codes f WHERE starts_with(f.code, v.code) OR starts_with(v.code, f.code))
  AND NOT EXISTS (SELECT 1 FROM paging_groups p WHERE starts_with(p.code, v.code) OR starts_with(v.code, p.code));

-- +goose Down
-- A destination an older version cannot express is dropped or reset, as the
-- earlier destination migrations do (migration 00040 for the same columns).
DELETE FROM inbound_routes WHERE destination_kind = 'voicemail_group';
UPDATE ivr_menus SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'voicemail_group';
UPDATE ivr_menus SET invalid_kind = 'hangup', invalid_target = '' WHERE invalid_kind = 'voicemail_group';
DELETE FROM ivr_menu_options WHERE dest_kind = 'voicemail_group';
UPDATE time_conditions SET open_kind = 'hangup', open_target = '', open_then = '' WHERE open_kind = 'voicemail_group';
UPDATE time_conditions SET closed_kind = 'hangup', closed_target = '', closed_then = '' WHERE closed_kind = 'voicemail_group';
UPDATE time_conditions SET holiday_kind = '', holiday_target = '', holiday_then = '' WHERE holiday_kind = 'voicemail_group';
UPDATE queues SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'voicemail_group';
UPDATE queues SET full_kind = 'hangup', full_target = '' WHERE full_kind = 'voicemail_group';
UPDATE queues SET no_agents_kind = 'hangup', no_agents_target = '' WHERE no_agents_kind = 'voicemail_group';
DELETE FROM feature_codes WHERE action = 'voicemail_broadcast';
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
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_open_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_closed_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('time_conditions', 'time_conditions_holiday_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_timeout_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_full_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('queues', 'queues_no_agents_kind_check', ARRAY[]::text[], ARRAY['voicemail_group']::text[]);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY[]::text[], ARRAY['voicemail_broadcast']::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);
DROP TABLE voicemail_group_members;
DROP TABLE voicemail_groups;
