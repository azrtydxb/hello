-- +goose Up
-- ACD call queues. A queue answers a call, holds it with music and periodic
-- announcements and offers it to the next free agent. Queue state (waiting
-- callers, agent login and pause) lives in Valkey; this migration holds only
-- the configuration and the CDR rows. Additive, so a rolling upgrade works:
-- an older hello-sip never reads these tables and never writes the legs.

CREATE TABLE queues (
    id                  BIGSERIAL   PRIMARY KEY,
    name                TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    -- The number that dials the queue directly (and that a transfer targets);
    -- empty means it is only reachable as a destination.
    number              TEXT        NOT NULL DEFAULT '' CHECK (number ~ '^([0-9]{2,16})?$'),
    enabled             BOOLEAN     NOT NULL DEFAULT TRUE,
    strategy            TEXT        NOT NULL DEFAULT 'ring_all'
        CHECK (strategy IN ('ring_all','longest_idle','round_robin','fewest_calls','random','linear')),
    -- Seconds an agent rings before the next one is tried, seconds before a
    -- call that nobody answered is offered again, and seconds an agent is
    -- left alone after a call.
    agent_timeout       INTEGER     NOT NULL DEFAULT 20 CHECK (agent_timeout BETWEEN 5 AND 120),
    retry_delay         INTEGER     NOT NULL DEFAULT 5 CHECK (retry_delay BETWEEN 0 AND 120),
    wrap_up             INTEGER     NOT NULL DEFAULT 0 CHECK (wrap_up BETWEEN 0 AND 600),
    -- 0 means no limit.
    max_callers         INTEGER     NOT NULL DEFAULT 0 CHECK (max_callers BETWEEN 0 AND 1000),
    max_wait            INTEGER     NOT NULL DEFAULT 600 CHECK (max_wait BETWEEN 0 AND 86400),
    -- Whether a call may join while no agent is logged in, and whether the
    -- callers waiting leave when the last agent logs out.
    join_empty          BOOLEAN     NOT NULL DEFAULT TRUE,
    leave_empty         BOOLEAN     NOT NULL DEFAULT FALSE,
    timeout_kind        TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (timeout_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','queue')),
    timeout_target      TEXT        NOT NULL DEFAULT '',
    full_kind           TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (full_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','queue')),
    full_target         TEXT        NOT NULL DEFAULT '',
    no_agents_kind      TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (no_agents_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','queue')),
    no_agents_target    TEXT        NOT NULL DEFAULT '',
    -- The music on hold class (migration 00029) the waiting caller hears;
    -- NULL means the global default.
    moh_class_id        BIGINT               REFERENCES moh_classes (id) ON DELETE SET NULL,
    -- Periodic announcements: every announce_frequency seconds (0 = never)
    -- the caller hears the periodic prompt, their position and the estimated
    -- wait. Numbers are spoken digit by digit from the announcements
    -- <digits_prefix>0 to <digits_prefix>9.
    announce_frequency  INTEGER     NOT NULL DEFAULT 0 CHECK (announce_frequency BETWEEN 0 AND 600),
    announce_position   BOOLEAN     NOT NULL DEFAULT FALSE,
    announce_wait       BOOLEAN     NOT NULL DEFAULT FALSE,
    periodic_prompt_id  BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    position_prompt_id  BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    wait_prompt_id      BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    minutes_prompt_id   BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    digits_prefix       TEXT        NOT NULL DEFAULT '' CHECK (digits_prefix ~ '^[A-Za-z0-9._-]{0,32}$'),
    -- The "press 1 to leave a voicemail" breakout: the key, the prompt that
    -- offers it and the extension whose box takes the message.
    breakout_key        TEXT        NOT NULL DEFAULT '' CHECK (breakout_key ~ '^[0-9*#]?$'),
    breakout_prompt_id  BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    breakout_voicemail  TEXT        NOT NULL DEFAULT '' CHECK (breakout_voicemail ~ '^([0-9]{2,10})?$'),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX queues_number ON queues (number) WHERE number <> '';

-- A queue's agents. A static member is always available (unless paused or
-- busy); a dynamic one must log in. A lower penalty is tried first; a higher
-- one only when every lower-penalty agent is unavailable. position orders
-- the linear strategy.
CREATE TABLE queue_members (
    queue_id     BIGINT  NOT NULL REFERENCES queues (id) ON DELETE CASCADE,
    extension_id BIGINT  NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    dynamic      BOOLEAN NOT NULL DEFAULT FALSE,
    penalty      INTEGER NOT NULL DEFAULT 0 CHECK (penalty BETWEEN 0 AND 99),
    position     INTEGER NOT NULL CHECK (position >= 1),
    PRIMARY KEY (queue_id, extension_id),
    UNIQUE (queue_id, position) DEFERRABLE INITIALLY DEFERRED
);

-- What a call did in a queue: entering it, each agent that rang, the wait,
-- the answering agent and the way out. One row per event, in order.
CREATE TABLE cdr_queue_legs (
    cdr_id    BIGINT      NOT NULL REFERENCES cdrs (id) ON DELETE CASCADE,
    position  INT         NOT NULL,
    event     TEXT        NOT NULL CHECK (event IN ('enter', 'agent', 'answered', 'wait', 'abandon', 'exit')),
    queue     TEXT        NOT NULL,
    extension TEXT        NOT NULL DEFAULT '',
    wait_ms   BIGINT      NOT NULL DEFAULT 0,
    result    TEXT        NOT NULL DEFAULT '',
    at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (cdr_id, position)
);

-- Widen the CHECK lists of the tables that name destinations. The lists are
-- read from the live constraint, so values other migrations added (which may
-- be applied in either order) are kept.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.hello_check_values(tbl regclass, cname text, add_vals text[], drop_vals text[])
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

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY['queue'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ring_groups', 'ring_groups_failure_kind_check', ARRAY['queue'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY['queue'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY['queue'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY['queue'], ARRAY[]::text[]);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check',
    ARRAY['queue_login', 'queue_logout', 'queue_pause', 'queue_unpause'], ARRAY[]::text[]);

-- Existing installs get the default agent codes when they are free (no other
-- feature code or page code equal to, a prefix of, or prefixed by them); a
-- fresh install (empty table) gets them from the control process's defaults
-- instead, so the "seed only an empty table" rule still holds.
INSERT INTO feature_codes (code, action, argument)
SELECT v.code, v.action, v.argument
FROM (VALUES ('*45', 'queue_login', ''), ('*46', 'queue_logout', ''),
             ('*47', 'queue_pause', 'break'), ('*48', 'queue_unpause', '')) AS v(code, action, argument)
WHERE EXISTS (SELECT 1 FROM feature_codes)
  AND NOT EXISTS (SELECT 1 FROM feature_codes f WHERE starts_with(f.code, v.code) OR starts_with(v.code, f.code))
  AND NOT EXISTS (SELECT 1 FROM paging_groups p WHERE starts_with(p.code, v.code) OR starts_with(v.code, p.code));

-- +goose Down
DELETE FROM inbound_routes WHERE destination_kind = 'queue';
UPDATE ring_groups SET failure_kind = 'none', failure_target = '' WHERE failure_kind = 'queue';
UPDATE ivr_menus SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'queue';
UPDATE ivr_menus SET invalid_kind = 'hangup', invalid_target = '' WHERE invalid_kind = 'queue';
DELETE FROM ivr_menu_options WHERE dest_kind = 'queue';
DELETE FROM feature_codes WHERE action IN ('queue_login', 'queue_logout', 'queue_pause', 'queue_unpause');

-- +goose StatementBegin
CREATE FUNCTION pg_temp.hello_check_values(tbl regclass, cname text, add_vals text[], drop_vals text[])
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

SELECT pg_temp.hello_check_values('inbound_routes', 'inbound_routes_destination_kind_check', ARRAY[]::text[], ARRAY['queue']);
SELECT pg_temp.hello_check_values('ring_groups', 'ring_groups_failure_kind_check', ARRAY[]::text[], ARRAY['queue']);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_timeout_kind_check', ARRAY[]::text[], ARRAY['queue']);
SELECT pg_temp.hello_check_values('ivr_menus', 'ivr_menus_invalid_kind_check', ARRAY[]::text[], ARRAY['queue']);
SELECT pg_temp.hello_check_values('ivr_menu_options', 'ivr_menu_options_dest_kind_check', ARRAY[]::text[], ARRAY['queue']);
SELECT pg_temp.hello_check_values('feature_codes', 'feature_codes_action_check',
    ARRAY[]::text[], ARRAY['queue_login', 'queue_logout', 'queue_pause', 'queue_unpause']);

DROP TABLE cdr_queue_legs;
DROP TABLE queue_members;
DROP TABLE queues;
