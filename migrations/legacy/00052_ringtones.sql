-- +goose Up
-- Tone library (spec phone-feature-parity S-43). A tone is a WAV the admin
-- uploads (PCM 8 or 16 kHz, mono, 16-bit, at most 30 s and 512 KiB), kept in
-- MinIO bucket hello-ringtones under object_key and served to a phone at
-- /p/<token>/ring/<name>.wav with sha256 as its ETag. A name is never reused
-- for other audio while the tone exists, so a phone that has a tone never
-- holds a stale one. A ring map choice of a custom tone is tone = 'custom'
-- with custom_tone_id naming the tone; the tone cannot be deleted while a
-- choice uses it. The provisioning fetch audit gets the kind ringtone. The
-- list is read from the live constraint, so values other migrations added
-- are kept. Additive, so a rolling upgrade works.
CREATE TABLE ringtones (
    id          BIGSERIAL   PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    object_key  TEXT        NOT NULL,
    size_bytes  INT         NOT NULL CHECK (size_bytes BETWEEN 1 AND 524288),
    sha256      TEXT        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    sample_rate INT         NOT NULL CHECK (sample_rate IN (8000, 16000)),
    duration_ms INT         NOT NULL CHECK (duration_ms BETWEEN 1 AND 30000),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE ring_map
    ADD COLUMN custom_tone_id BIGINT REFERENCES ringtones (id) ON DELETE RESTRICT,
    ADD CONSTRAINT ring_map_custom_tone CHECK ((tone = 'custom') = (custom_tone_id IS NOT NULL));
CREATE INDEX ring_map_custom_tone ON ring_map (custom_tone_id) WHERE custom_tone_id IS NOT NULL;

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

SELECT pg_temp.hello_check_values('prov_fetches', 'prov_fetches_kind_check',
    ARRAY['ringtone'], ARRAY[]::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

-- +goose Down
-- Fetch rows of the kind an older version cannot express are removed, as
-- earlier kind migrations do; custom tone choices go with the table.
DELETE FROM prov_fetches WHERE kind = 'ringtone';
DELETE FROM ring_map WHERE tone = 'custom';
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

SELECT pg_temp.hello_check_values('prov_fetches', 'prov_fetches_kind_check',
    ARRAY[]::text[], ARRAY['ringtone']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);
DROP INDEX ring_map_custom_tone;
ALTER TABLE ring_map DROP CONSTRAINT ring_map_custom_tone, DROP COLUMN custom_tone_id;
DROP TABLE ringtones;
