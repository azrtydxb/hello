-- +goose Up
-- Fax (pbx-gaps S-15, S-16, S-17). Receive and send faxes over G.711 or T.38,
-- decoded by the separate hello-fax service, stored as PDF in the media bucket
-- under fax/<yyyy>/<id>.pdf and delivered by email. Additive: an older
-- hello-sip never reads the new table or columns, a trunk keeps passthrough
-- (the carrier-neutral default), and a route or IVR key of the new "fax" kind
-- only exists once the API accepts one.
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

-- How a trunk carries a fax: audio passthrough (default), T.38 when the
-- carrier offers it, or off (fax calls on the trunk are refused with 488).
ALTER TABLE trunks
    ADD COLUMN fax_mode TEXT NOT NULL DEFAULT 'passthrough' CHECK (fax_mode IN ('passthrough', 't38', 'off'));

-- A fax destination names the extension that owns the faxes arriving there
-- and, optionally, an address the PDF is emailed to instead of the owner's.
CREATE TABLE fax_destinations (
    id           BIGSERIAL   PRIMARY KEY,
    name         TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,40}$'),
    extension_id BIGINT      NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    email        TEXT        NOT NULL DEFAULT '' CHECK (length(email) <= 254),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per fax. An inbound fax is inserted when the call is answered
-- (status receiving) and finished when T.30 ends; an outbound one is queued by
-- the API and claimed by a hello-sip node under a lease, like a queue callback.
-- status: queued and sending (outbound), receiving (inbound), received, sent,
-- partial (some pages arrived or went), failed, cancelled.
-- mail_status: none (nothing to send), pending, sending, sent, failed.
CREATE TABLE faxes (
    id              BIGSERIAL   PRIMARY KEY,
    direction       TEXT        NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    status          TEXT        NOT NULL CHECK (status IN ('queued', 'sending', 'receiving', 'received', 'sent', 'partial', 'failed', 'cancelled')),
    peer_number     TEXT        NOT NULL DEFAULT '' CHECK (length(peer_number) <= 40),
    local_number    TEXT        NOT NULL DEFAULT '' CHECK (length(local_number) <= 40),
    destination_id  BIGINT      REFERENCES fax_destinations (id) ON DELETE SET NULL,
    owner_extension TEXT        NOT NULL DEFAULT '' CHECK (length(owner_extension) <= 10),
    pages           INTEGER     NOT NULL DEFAULT 0 CHECK (pages >= 0),
    mode            TEXT        NOT NULL DEFAULT '' CHECK (mode IN ('', 'g711', 't38')),
    bit_rate        INTEGER     NOT NULL DEFAULT 0,
    result_code     INTEGER,
    result_text     TEXT        NOT NULL DEFAULT '' CHECK (length(result_text) <= 200),
    object_key      TEXT        NOT NULL DEFAULT '',
    correlation_id  TEXT        NOT NULL DEFAULT '',
    created_by      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    -- Outbound queue.
    trunk_name      TEXT        NOT NULL DEFAULT '',
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_by      TEXT        NOT NULL DEFAULT '',
    lease_until     TIMESTAMPTZ,
    -- Email delivery of a received fax.
    mail_to         TEXT        NOT NULL DEFAULT '',
    mail_status     TEXT        NOT NULL DEFAULT 'none' CHECK (mail_status IN ('none', 'pending', 'sending', 'sent', 'failed')),
    mail_attempts   INTEGER     NOT NULL DEFAULT 0,
    mail_next_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    mail_error      TEXT        NOT NULL DEFAULT '' CHECK (length(mail_error) <= 200)
);
CREATE INDEX faxes_created_idx ON faxes (created_at DESC, id DESC);
CREATE INDEX faxes_owner_idx ON faxes (owner_extension, created_at DESC);
CREATE INDEX faxes_queue_idx ON faxes (next_attempt_at) WHERE status = 'queued';
CREATE INDEX faxes_mail_idx ON faxes (mail_next_at) WHERE mail_status IN ('pending', 'sending');
CREATE INDEX faxes_corr_idx ON faxes (correlation_id) WHERE correlation_id <> '';

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY['fax']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY['fax']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY['fax']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY['fax']::text[], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY['fax']::text[], ARRAY[]::text[]);
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

-- A destination of the removed kind cannot be expressed by an older version:
-- routes and keys using it are removed, fax CDRs become plain calls.
DELETE FROM inbound_routes WHERE destination_kind = 'fax';
DELETE FROM ivr_menu_options WHERE dest_kind = 'fax';
UPDATE ivr_menus SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'fax';
UPDATE ivr_menus SET invalid_kind = 'hangup', invalid_target = '' WHERE invalid_kind = 'fax';
UPDATE cdrs SET kind = 'call' WHERE kind = 'fax';
SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY[]::text[], ARRAY['fax']::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY[]::text[], ARRAY['fax']::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY[]::text[], ARRAY['fax']::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY[]::text[], ARRAY['fax']::text[]);
SELECT pg_temp.hello_check_values('cdrs', 'cdrs_kind_check', ARRAY[]::text[], ARRAY['fax']::text[]);
DROP FUNCTION pg_temp.hello_check_values(regclass, text, text[], text[]);

DROP TABLE faxes;
DROP TABLE fax_destinations;
ALTER TABLE trunks DROP COLUMN fax_mode;
