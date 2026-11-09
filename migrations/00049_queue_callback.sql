-- +goose Up
-- Queue callback (pbx-gaps S-9, S-10) and the queue's prompt language (S-20).
-- A queue can offer a waiting caller a call back instead of holding the line;
-- the request is a durable row, so it outlives the call, a restart and the
-- loss of a node. Additive: an older hello-sip never reads these columns or
-- the table, and a queue with callback_enabled false behaves as before.

ALTER TABLE queues
    ADD COLUMN callback_enabled             BOOLEAN NOT NULL DEFAULT FALSE,
    -- Offer a callback once the estimated wait exceeds this many seconds.
    ADD COLUMN callback_offer_after_seconds INTEGER NOT NULL DEFAULT 120
        CHECK (callback_offer_after_seconds BETWEEN 0 AND 3600),
    -- The key that accepts the offer, and that asks for one at any time.
    ADD COLUMN callback_digit               TEXT    NOT NULL DEFAULT '2' CHECK (callback_digit ~ '^[0-9*#]$'),
    -- NULL inherits: the deployment language, then English (same resolution
    -- as a voicemail box). A value is a language with a complete table in
    -- internal/voiceprompt; the API validates it, so no CHECK here.
    ADD COLUMN prompt_language              TEXT;

-- One row per callback request. status: waiting (due at next_attempt_at),
-- dialling (claimed by claimed_by until lease_until), connected, failed,
-- cancelled, expired. position_key is the caller's original enqueue time
-- (Unix ms): in the queue's ordering the callback sorts by it, so it is
-- served as if it had never left.
CREATE TABLE queue_callbacks (
    id              BIGSERIAL   PRIMARY KEY,
    queue_id        BIGINT      NOT NULL REFERENCES queues (id) ON DELETE CASCADE,
    number          TEXT        NOT NULL CHECK (number ~ '^\+?[0-9]{2,20}$'),
    caller_name     TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL DEFAULT 'waiting'
        CHECK (status IN ('waiting', 'dialling', 'connected', 'failed', 'cancelled', 'expired')),
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    position_key    BIGINT      NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_by      TEXT        NOT NULL DEFAULT '',
    lease_until     TIMESTAMPTZ,
    agent           TEXT        NOT NULL DEFAULT '',
    result          TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    finished_at     TIMESTAMPTZ
);
-- A number waits at most once per queue: a second request is coalesced.
CREATE UNIQUE INDEX queue_callbacks_open_number ON queue_callbacks (queue_id, number)
    WHERE status IN ('waiting', 'dialling');
CREATE INDEX queue_callbacks_due ON queue_callbacks (queue_id, position_key)
    WHERE status IN ('waiting', 'dialling');
CREATE INDEX queue_callbacks_finished ON queue_callbacks (finished_at) WHERE finished_at IS NOT NULL;

-- The callback events of a call's queue legs, and the callback call kind. The
-- lists are read from the live constraints, so values other migrations added
-- are kept.
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

SELECT pg_temp.hello_check_values('cdr_queue_legs', 'cdr_queue_legs_event_check',
    ARRAY['callback_requested', 'callback_dialled', 'callback_connected', 'callback_failed'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY['callback'], ARRAY[]::text[]);
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

DELETE FROM cdr_queue_legs WHERE event IN ('callback_requested', 'callback_dialled', 'callback_connected', 'callback_failed');
UPDATE cdrs SET kind = 'call' WHERE kind = 'callback';
SELECT pg_temp.hello_check_values('cdr_queue_legs', 'cdr_queue_legs_event_check',
    ARRAY[]::text[], ARRAY['callback_requested', 'callback_dialled', 'callback_connected', 'callback_failed']);
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY[]::text[], ARRAY['callback']);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

DROP TABLE queue_callbacks;
ALTER TABLE queues
    DROP COLUMN prompt_language,
    DROP COLUMN callback_digit,
    DROP COLUMN callback_offer_after_seconds,
    DROP COLUMN callback_enabled;
