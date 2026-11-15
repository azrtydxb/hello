-- +goose Up
-- Messaging channels, phase 8 (spec messaging-channels M-20, M-21, M-23):
-- integrations, reporting and privacy tooling. Everything is additive, so a
-- rolling upgrade works.

-- Webhook endpoints choose whether an inbox event carries the customer's
-- number (otherwise only a keyed hash) and the message text (otherwise
-- never). Both are off by default; only an administrator sets them and the
-- change is audited.
ALTER TABLE webhook_endpoints
    ADD COLUMN include_peer         BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN include_message_body BOOLEAN NOT NULL DEFAULT FALSE;

-- Service-level targets the reports measure against: the first answer to a
-- customer and the resolution, in seconds after the conversation started.
-- 0 turns a target off.
ALTER TABLE messaging_settings
    ADD COLUMN sla_first_response_seconds INT NOT NULL DEFAULT 900 CHECK (sla_first_response_seconds BETWEEN 0 AND 604800),
    ADD COLUMN sla_resolution_seconds     INT NOT NULL DEFAULT 86400 CHECK (sla_resolution_seconds BETWEEN 0 AND 2592000);

-- A message an administrator redacted: its text is gone (body is empty) and
-- the thread shows that it was redacted, when and by whom (audited).
ALTER TABLE conversation_messages ADD COLUMN redacted_at TIMESTAMPTZ;

-- Provider-side deletion of texts Hello sent through a carrier API. An erase
-- or a redaction queues one row per carrier message in its own transaction,
-- so a carrier outage loses nothing; the request tries them at once and a
-- worker retries what is left. The row holds the carrier's message id only,
-- never text or a number, and is dropped 30 days after it finished.
CREATE TABLE msg_provider_redactions (
    id                  BIGSERIAL   PRIMARY KEY,
    channel_id          BIGINT      NOT NULL REFERENCES msg_channels (id) ON DELETE CASCADE,
    provider            TEXT        NOT NULL CHECK (length(provider) BETWEEN 1 AND 32),
    provider_message_id TEXT        NOT NULL CHECK (length(provider_message_id) BETWEEN 1 AND 128),
    -- batch names the erase or redaction that queued the row.
    batch               TEXT        NOT NULL,
    state               TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed', 'unsupported')),
    attempts            INT         NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_until       TIMESTAMPTZ,
    last_error          TEXT        NOT NULL DEFAULT '' CHECK (length(last_error) <= 200),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ,
    UNIQUE (channel_id, provider_message_id)
);
CREATE INDEX msg_provider_redactions_due ON msg_provider_redactions (next_attempt_at) WHERE state = 'pending';
CREATE INDEX msg_provider_redactions_batch ON msg_provider_redactions (batch);
CREATE INDEX msg_provider_redactions_finished ON msg_provider_redactions (finished_at) WHERE finished_at IS NOT NULL;

-- The reports read conversations and messages by when they started.
CREATE INDEX conversations_created ON conversations (created_at);
CREATE INDEX conversation_messages_created ON conversation_messages (created_at) WHERE kind = 'message';

-- The supervisor-view audit report reads these rows by time.
CREATE INDEX audit_events_supervisor_view ON audit_events (at) WHERE action = 'supervisor_view';

-- +goose Down
DROP INDEX audit_events_supervisor_view;
DROP INDEX conversation_messages_created;
DROP INDEX conversations_created;
DROP TABLE msg_provider_redactions;
ALTER TABLE conversation_messages DROP COLUMN redacted_at;
ALTER TABLE messaging_settings DROP COLUMN sla_resolution_seconds, DROP COLUMN sla_first_response_seconds;
ALTER TABLE webhook_endpoints DROP COLUMN include_message_body, DROP COLUMN include_peer;
