-- +goose Up
-- Messaging channels, phase 5 (spec messaging-channels M-13, M-14): routing
-- and ACD for the unified inbox. A number (msg_addresses) says where its new
-- conversations go (a queue, a user, an extension's user, a ring group's
-- users, or the supervisors' pool) and when it is open; a queue says whether
-- it takes texts, how many each agent holds and when an unanswered one goes
-- back to the pool. The router reads agent availability from the same
-- login/pause state the call queues use. Everything is additive, so a
-- rolling upgrade works: a number keeps routing to the pool until an
-- administrator picks something else, and a queue keeps taking calls only.

ALTER TABLE queues
    ADD COLUMN text_enabled            BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN text_capacity           INT     NOT NULL DEFAULT 3   CHECK (text_capacity BETWEEN 1 AND 50),
    -- 0 turns the return of unanswered conversations off.
    ADD COLUMN text_reassign_seconds   INT     NOT NULL DEFAULT 300 CHECK (text_reassign_seconds BETWEEN 0 AND 86400),
    ADD COLUMN text_requires_idle_voice BOOLEAN NOT NULL DEFAULT FALSE;

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

-- A one-value IN list is stored as an equality, which the helper does not
-- read: widen it by one value first (queue, which is additive too), then let
-- the helper add the rest.
ALTER TABLE msg_addresses DROP CONSTRAINT msg_addresses_route_kind_check;
ALTER TABLE msg_addresses ADD CONSTRAINT msg_addresses_route_kind_check CHECK (route_kind IN ('inbox', 'queue'));
SELECT pg_temp.hello_check_values('msg_addresses', 'msg_addresses_route_kind_check', ARRAY['user', 'extension', 'ring_group']::text[], ARRAY[]::text[]);

-- route_kind and route_target (the id of the queue, user, extension or ring
-- group, as text) exist since the inbox core. The schedule is a weekly
-- window (zone and days, empty means always open); outside it the
-- after-hours action applies: none (route as usual), reply (send the text,
-- then route as usual) or route (to the other target).
ALTER TABLE msg_addresses
    ADD COLUMN schedule                JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN after_hours_action      TEXT  NOT NULL DEFAULT 'none' CHECK (after_hours_action IN ('none', 'reply', 'route')),
    ADD COLUMN after_hours_text        TEXT  NOT NULL DEFAULT '' CHECK (length(after_hours_text) <= 480),
    ADD COLUMN after_hours_route_kind  TEXT  NOT NULL DEFAULT 'inbox' CHECK (after_hours_route_kind IN ('inbox', 'queue', 'user', 'extension', 'ring_group')),
    ADD COLUMN after_hours_route_target TEXT NOT NULL DEFAULT '',
    -- Sent once to a customer who texts the number for the first time.
    ADD COLUMN first_contact_text      TEXT  NOT NULL DEFAULT '' CHECK (length(first_contact_text) <= 480);

-- Where a conversation stands in routing. routed_at is NULL until the
-- router has placed it (any channel's ingest leaves it NULL, so a channel
-- that merges later is routed without a change of its own). queue_id and
-- ring_group_id name the pool an unassigned conversation waits in.
ALTER TABLE conversations
    ADD COLUMN queue_id       BIGINT REFERENCES queues (id) ON DELETE SET NULL,
    ADD COLUMN ring_group_id  BIGINT REFERENCES ring_groups (id) ON DELETE SET NULL,
    ADD COLUMN routed_at      TIMESTAMPTZ,
    ADD COLUMN assigned_at    TIMESTAMPTZ,
    -- Set once the first response has been counted in the metric.
    ADD COLUMN frt_counted    BOOLEAN NOT NULL DEFAULT FALSE;
-- What exists is placed already: only conversations that arrive from now on
-- are routed.
UPDATE conversations SET routed_at = created_at, frt_counted = TRUE;
CREATE INDEX conversations_unrouted ON conversations (id) WHERE routed_at IS NULL;
CREATE INDEX conversations_queue_waiting ON conversations (queue_id, routed_at, id)
    WHERE queue_id IS NOT NULL AND assignee_user_id IS NULL AND status IN ('open', 'pending');
CREATE INDEX conversations_ring_group ON conversations (ring_group_id)
    WHERE ring_group_id IS NOT NULL AND assignee_user_id IS NULL;
CREATE INDEX conversations_frt ON conversations (id) WHERE NOT frt_counted AND first_response_at IS NOT NULL;

-- What the text assignments have given each agent, for the strategies
-- (fewest_calls, longest_idle). Separate from the voice statistics so a text
-- does not skew a call queue. PostgreSQL is the record.
CREATE TABLE msg_agent_stats (
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    queue_id      BIGINT      NOT NULL REFERENCES queues (id) ON DELETE CASCADE,
    text_answered BIGINT      NOT NULL DEFAULT 0,
    last_end      TIMESTAMPTZ,
    PRIMARY KEY (user_id, queue_id)
);

-- The round robin counter of a queue's texts.
CREATE TABLE msg_queue_rr (
    queue_id BIGINT PRIMARY KEY REFERENCES queues (id) ON DELETE CASCADE,
    rr       BIGINT NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE msg_queue_rr;
DROP TABLE msg_agent_stats;
DROP INDEX conversations_frt;
DROP INDEX conversations_ring_group;
DROP INDEX conversations_queue_waiting;
DROP INDEX conversations_unrouted;
ALTER TABLE conversations
    DROP COLUMN frt_counted, DROP COLUMN assigned_at, DROP COLUMN routed_at,
    DROP COLUMN ring_group_id, DROP COLUMN queue_id;
ALTER TABLE msg_addresses
    DROP COLUMN first_contact_text, DROP COLUMN after_hours_route_target, DROP COLUMN after_hours_route_kind, DROP COLUMN after_hours_text,
    DROP COLUMN after_hours_action, DROP COLUMN schedule;
UPDATE msg_addresses SET route_kind = 'inbox', route_target = '';
ALTER TABLE msg_addresses DROP CONSTRAINT msg_addresses_route_kind_check;
ALTER TABLE msg_addresses ADD CONSTRAINT msg_addresses_route_kind_check CHECK (route_kind IN ('inbox'));
ALTER TABLE queues
    DROP COLUMN text_requires_idle_voice, DROP COLUMN text_reassign_seconds, DROP COLUMN text_capacity, DROP COLUMN text_enabled;
