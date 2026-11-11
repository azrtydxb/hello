-- +goose Up
-- Follow-me (pbx-gaps B1: S-5, S-6). An extension's profile rings its own
-- devices and up to five more targets (an external number, another extension
-- or a ring group), simultaneously or one after the other. Every table is new
-- and every other change is additive, so a rolling upgrade works: an older
-- hello-sip ignores the tables and never sees a follow-me feature code.

CREATE TABLE follow_me (
    extension_id     BIGINT      PRIMARY KEY REFERENCES extensions (id) ON DELETE CASCADE,
    enabled          BOOLEAN     NOT NULL DEFAULT FALSE,
    strategy         TEXT        NOT NULL DEFAULT 'simultaneous' CHECK (strategy IN ('simultaneous', 'sequential')),
    ring_own_devices BOOLEAN     NOT NULL DEFAULT TRUE,
    timeout_seconds  INTEGER     NOT NULL DEFAULT 30 CHECK (timeout_seconds BETWEEN 5 AND 120),
    confirm_external BOOLEAN     NOT NULL DEFAULT TRUE,
    dnd_blocks       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE follow_me_targets (
    extension_id  BIGINT   NOT NULL REFERENCES follow_me (extension_id) ON DELETE CASCADE,
    position      SMALLINT NOT NULL CHECK (position BETWEEN 1 AND 5),
    kind          TEXT     NOT NULL CHECK (kind IN ('external', 'extension', 'ring_group')),
    target        TEXT     NOT NULL CHECK (target <> '' AND length(target) <= 64),
    delay_seconds INTEGER  NOT NULL DEFAULT 0 CHECK (delay_seconds BETWEEN 0 AND 60),
    PRIMARY KEY (extension_id, position)
);

-- One row per leg a call placed outside the ring of the dialled extension
-- (pbx-gaps S-22). Follow-me writes the role follow_me; the codec columns are
-- the codec-per-leg item's. IF NOT EXISTS: whichever of the two lands first
-- creates the table, the shape is the spec's.
CREATE TABLE IF NOT EXISTS cdr_legs (
    cdr_id       BIGINT NOT NULL REFERENCES cdrs (id) ON DELETE CASCADE,
    position     INT    NOT NULL,
    role         TEXT   NOT NULL,
    extension    TEXT   NOT NULL DEFAULT '',
    trunk        TEXT   NOT NULL DEFAULT '',
    codec        TEXT,
    payload_type INT,
    result       TEXT   NOT NULL DEFAULT '',
    target_kind  TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (cdr_id, position)
);
ALTER TABLE cdr_legs ADD COLUMN IF NOT EXISTS result TEXT NOT NULL DEFAULT '';
ALTER TABLE cdr_legs ADD COLUMN IF NOT EXISTS target_kind TEXT NOT NULL DEFAULT '';

-- The feature-code actions follow_me_on and follow_me_off. The list is read
-- from the live constraint, so values other migrations added are kept.
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
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY['follow_me_on', 'follow_me_off']::text[], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- Existing installs get the default codes *76 (on) and *77 (off) when they are
-- free (no other feature code or page code equal to, a prefix of, or prefixed
-- by them); a fresh install (empty table) gets them from the control
-- process's defaults instead, so the "seed only an empty table" rule holds.
-- The spec named *74 and *75, which are the park defaults.
INSERT INTO feature_codes (code, action, argument)
SELECT v.code, v.action, v.argument
FROM (VALUES ('*76', 'follow_me_on', ''), ('*77', 'follow_me_off', '')) AS v(code, action, argument)
WHERE EXISTS (SELECT 1 FROM feature_codes)
  AND NOT EXISTS (SELECT 1 FROM feature_codes f WHERE starts_with(f.code, v.code) OR starts_with(v.code, f.code))
  AND NOT EXISTS (SELECT 1 FROM paging_groups p WHERE starts_with(p.code, v.code) OR starts_with(v.code, p.code));

-- +goose Down
DELETE FROM feature_codes WHERE action IN ('follow_me_on', 'follow_me_off');
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
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check', ARRAY[]::text[], ARRAY['follow_me_on', 'follow_me_off']::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);
DELETE FROM cdr_legs WHERE role = 'follow_me';
ALTER TABLE cdr_legs DROP COLUMN target_kind, DROP COLUMN result;
DROP TABLE follow_me_targets;
DROP TABLE follow_me;
