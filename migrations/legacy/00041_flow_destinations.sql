-- +goose Up
-- Unified destinations (spec flow-canvas, decision 3). An inbound route can
-- now send a call straight to a ring group (by name, so the name need not be
-- numeric), a voicemail box (the extension number), an announcement or a
-- paging group (by code). The list is read from the live constraint, so
-- values other migrations added (applied in either order) are kept. No row changes: existing routes keep their kind and behaviour
-- (an "extension" route whose destination names a ring group still rings
-- the group). Additive, so a rolling upgrade works: an older hello-sip never
-- sees a row of a new kind until the API accepts one.
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

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check',
    ARRAY['ring_group', 'voicemail', 'announcement', 'paging'], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- +goose Down
-- A ring-group route is the same call as an "extension" route naming the
-- group (the lookup order is group name first), so it is converted rather
-- than lost; the kinds an older version cannot express are removed, as the
-- earlier destination migrations do.
UPDATE inbound_routes SET destination_kind = 'extension' WHERE destination_kind = 'ring_group';
DELETE FROM inbound_routes WHERE destination_kind IN ('voicemail', 'announcement', 'paging');
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
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check',
    ARRAY[]::text[], ARRAY['ring_group', 'voicemail', 'announcement', 'paging']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);
