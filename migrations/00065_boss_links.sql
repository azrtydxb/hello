-- +goose Up
-- Boss and secretary (pbx-gaps S-18, S-19). A boss has at most one assistant
-- link; an assistant may serve many bosses. Calls to the boss ring the boss's
-- and the assistant's devices (both, or the assistant first), screening plays
-- the assistant a prompt before the call is put through, and the *64 feature
-- code toggles divert_on. Additive, so a rolling upgrade works: an older
-- hello-sip never reads the table and ignores the column.

CREATE TABLE boss_links (
    id                    BIGSERIAL   PRIMARY KEY,
    boss_extension_id     BIGINT      NOT NULL UNIQUE REFERENCES extensions (id) ON DELETE CASCADE,
    assistant_extension_id BIGINT     NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    ring_mode             TEXT        NOT NULL DEFAULT 'both' CHECK (ring_mode IN ('both', 'assistant_first')),
    screening             BOOLEAN     NOT NULL DEFAULT FALSE,
    enabled               BOOLEAN     NOT NULL DEFAULT TRUE,
    divert_on             BOOLEAN     NOT NULL DEFAULT FALSE,
    outbound_as_boss      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (boss_extension_id <> assistant_extension_id)
);
CREATE INDEX boss_links_assistant ON boss_links (assistant_extension_id);

-- The extension that answered when it was not the one dialled (the assistant
-- of a boss link); empty otherwise.
ALTER TABLE cdrs ADD COLUMN answered_by TEXT NOT NULL DEFAULT '';

-- The *64 divert code. The list is read from the live constraint, so values
-- other migrations added (which may be applied in either order) are kept.
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

SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check',
    ARRAY['boss_divert'], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- Existing installs get *64 when it is free (no other feature code or page
-- code equal to, a prefix of, or prefixed by it); a fresh install (empty
-- table) gets it from the control process's defaults instead, so the "seed
-- only an empty table" rule still holds.
INSERT INTO feature_codes (code, action, argument)
SELECT v.code, v.action, v.argument
FROM (VALUES ('*64', 'boss_divert', '')) AS v(code, action, argument)
WHERE EXISTS (SELECT 1 FROM feature_codes)
  AND NOT EXISTS (SELECT 1 FROM feature_codes f WHERE starts_with(f.code, v.code) OR starts_with(v.code, f.code))
  AND NOT EXISTS (SELECT 1 FROM paging_groups p WHERE starts_with(p.code, v.code) OR starts_with(v.code, p.code));

-- +goose Down
DELETE FROM feature_codes WHERE action = 'boss_divert';

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

SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check',
    ARRAY[]::text[], ARRAY['boss_divert']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

ALTER TABLE cdrs DROP COLUMN answered_by;
DROP TABLE boss_links;
