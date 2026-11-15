-- +goose Up
-- Messaging channels, phase 7 (spec messaging-channels M-30, M-30a): AI
-- triage, reply drafts and auto-reply in the unified inbox, all through
-- internal/ai (go-ai-sdk). Everything is additive, so a rolling upgrade
-- works: every number starts with AI off, nothing is queued for it, and the
-- inbox behaves as before until an administrator turns a mode on.
--
-- Three kill switches stop the AI at three levels: messaging_settings
-- (ai_enabled for all of it, ai_auto_enabled for auto-replies), a channel
-- (msg_channels.ai_enabled) and a number or chat site (msg_addresses.ai_mode).
-- HELLO_MSG_AI_AUTO=false stops auto-replies for the whole deployment.

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

-- A number may route through the AI: triage first (at most a few seconds),
-- then the queue the triage suggests, or the route's own queue.
SELECT pg_temp.hello_check_values('msg_addresses', 'msg_addresses_route_kind_check', ARRAY['ai_triage']::text[], ARRAY[]::text[]);

-- The AI of one number (or chat site). ai_mode: off, triage (classify and
-- summarise, nothing the customer sees), draft (triage plus a suggested reply
-- an agent must click to use), auto (the AI may answer from the number's
-- FAQ, within the guardrails below).
ALTER TABLE msg_addresses
    ADD COLUMN ai_mode            TEXT    NOT NULL DEFAULT 'off' CHECK (ai_mode IN ('off', 'triage', 'draft', 'auto')),
    -- Replace phone-number-like digit runs and email addresses before a call.
    ADD COLUMN ai_redact          BOOLEAN NOT NULL DEFAULT TRUE,
    -- The first AI reply of a conversation starts with the disclosure line.
    ADD COLUMN ai_disclosure      BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN ai_disclosure_text TEXT    NOT NULL DEFAULT 'This is an automated reply.' CHECK (length(ai_disclosure_text) <= 160),
    -- Sent (once until a person replies) when the AI hands a conversation to
    -- a person; empty sends nothing.
    ADD COLUMN ai_holding_text    TEXT    NOT NULL DEFAULT '' CHECK (length(ai_holding_text) <= 480),
    -- AI replies before a person must answer, and AI sends per hour.
    ADD COLUMN ai_max_replies     INT     NOT NULL DEFAULT 3 CHECK (ai_max_replies BETWEEN 1 AND 10),
    ADD COLUMN ai_hourly_cap      INT     NOT NULL DEFAULT 60 CHECK (ai_hourly_cap BETWEEN 1 AND 1000),
    -- The least confidence (0 to 1) of the triage and of the answer.
    ADD COLUMN ai_min_confidence  REAL    NOT NULL DEFAULT 0.8 CHECK (ai_min_confidence BETWEEN 0 AND 1),
    -- The intents the AI may answer; empty allows every intent but "other".
    ADD COLUMN ai_auto_intents    TEXT[]  NOT NULL DEFAULT '{}',
    -- A customer text with one of these words goes to a person.
    ADD COLUMN ai_handoff_words   TEXT[]  NOT NULL DEFAULT '{agent,human,person,representative}';

ALTER TABLE msg_channels
    ADD COLUMN ai_enabled BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE messaging_settings
    ADD COLUMN ai_enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN ai_auto_enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN ai_business_name    TEXT    NOT NULL DEFAULT '' CHECK (length(ai_business_name) <= 100),
    -- The intents the triage picks from ("other" is always allowed).
    ADD COLUMN ai_intents          TEXT[]  NOT NULL DEFAULT '{question,sales,support,billing,appointment,complaint}',
    -- Topics the AI never answers: a text about one goes to a person.
    ADD COLUMN ai_sensitive_topics TEXT[]  NOT NULL DEFAULT '{medical,legal,emergency,self-harm,harassment,payment dispute,personal data}';

-- What the AI made of a conversation: the last triage (no customer text but
-- the model's own summary), what it changed (for revert), and the flags.
ALTER TABLE conversations
    ADD COLUMN ai_triage         JSONB,
    ADD COLUMN ai_applied        JSONB       NOT NULL DEFAULT '{}',
    ADD COLUMN ai_spam           BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN ai_handoff_at     TIMESTAMPTZ,
    ADD COLUMN ai_handoff_reason TEXT        NOT NULL DEFAULT '';

-- One AI run per inbound message and kind. Any control replica claims a
-- queued run (SKIP LOCKED); a run whose replica died is claimed again after
-- two minutes, at most three times. result holds the model's validated
-- answer; error a code, never text.
CREATE TABLE ai_message_runs (
    id              BIGSERIAL   PRIMARY KEY,
    message_id      BIGINT      NOT NULL REFERENCES conversation_messages (id) ON DELETE CASCADE,
    conversation_id BIGINT      NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('triage', 'draft', 'autoreply')),
    state           TEXT        NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed', 'skipped', 'stale')),
    result          JSONB,
    error           TEXT        NOT NULL DEFAULT '',
    attempts        INT         NOT NULL DEFAULT 0,
    claimed_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    UNIQUE (message_id, kind)
);
CREATE INDEX ai_message_runs_open ON ai_message_runs (id) WHERE state IN ('queued', 'running');
CREATE INDEX ai_message_runs_conv ON ai_message_runs (conversation_id, kind, id DESC);
CREATE INDEX ai_message_runs_created ON ai_message_runs (created_at);

-- The AI message that a run sent (an auto-reply, or the holding line).
ALTER TABLE conversation_messages ADD COLUMN ai_run_id BIGINT REFERENCES ai_message_runs (id) ON DELETE SET NULL;
CREATE INDEX conversation_messages_ai ON conversation_messages (created_at) WHERE sender_kind = 'ai';

-- The FAQ an auto-reply may answer from: at most 50 per number, 4 KB each,
-- written by an administrator.
CREATE TABLE msg_faq (
    id         BIGSERIAL   PRIMARY KEY,
    address_id BIGINT      NOT NULL REFERENCES msg_addresses (id) ON DELETE CASCADE,
    title      TEXT        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body       TEXT        NOT NULL CHECK (length(body) BETWEEN 1 AND 4096),
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX msg_faq_address ON msg_faq (address_id, id);

-- +goose Down
DROP TABLE msg_faq;
DROP INDEX conversation_messages_ai;
ALTER TABLE conversation_messages DROP COLUMN ai_run_id;
DROP TABLE ai_message_runs;
ALTER TABLE conversations
    DROP COLUMN ai_handoff_reason, DROP COLUMN ai_handoff_at, DROP COLUMN ai_spam, DROP COLUMN ai_applied, DROP COLUMN ai_triage;
ALTER TABLE messaging_settings
    DROP COLUMN ai_sensitive_topics, DROP COLUMN ai_intents, DROP COLUMN ai_business_name, DROP COLUMN ai_auto_enabled, DROP COLUMN ai_enabled;
ALTER TABLE msg_channels DROP COLUMN ai_enabled;
ALTER TABLE msg_addresses
    DROP COLUMN ai_handoff_words, DROP COLUMN ai_auto_intents, DROP COLUMN ai_min_confidence, DROP COLUMN ai_hourly_cap,
    DROP COLUMN ai_max_replies, DROP COLUMN ai_holding_text, DROP COLUMN ai_disclosure_text, DROP COLUMN ai_disclosure,
    DROP COLUMN ai_redact, DROP COLUMN ai_mode;
UPDATE msg_addresses SET route_kind = 'queue' WHERE route_kind = 'ai_triage';
ALTER TABLE msg_addresses DROP CONSTRAINT msg_addresses_route_kind_check;
ALTER TABLE msg_addresses ADD CONSTRAINT msg_addresses_route_kind_check CHECK (route_kind IN ('inbox', 'queue', 'user', 'extension', 'ring_group'));
