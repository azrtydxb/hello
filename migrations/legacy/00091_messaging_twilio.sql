-- +goose Up
-- Messaging channels, delivery item 2 (spec messaging-channels, "outbound
-- sending on Twilio"): a channel can reach a carrier's web API instead of a
-- SIP trunk. Everything is additive, so a rolling upgrade works: no
-- existing row or CHECK constraint changes, and a SIP trunk channel behaves
-- as before.
--
-- Credentials (the carrier account and its auth token) are sealed with the
-- secret box into msg_channels.credentials_enc, which already exists; the
-- config column keeps only a non-secret hint. Nothing here stores a message
-- body outside conversation_messages, and no credential outside that column.

-- First contact (M-17): off by default; when an administrator turns it on,
-- every new outbound conversation needs an attestation and is rate limited.
ALTER TABLE messaging_settings
    ADD COLUMN first_contact TEXT NOT NULL DEFAULT 'off' CHECK (first_contact IN ('off', 'attested')),
    ADD COLUMN first_contact_user_hour INT NOT NULL DEFAULT 20 CHECK (first_contact_user_hour BETWEEN 1 AND 1000),
    ADD COLUMN first_contact_address_day INT NOT NULL DEFAULT 200 CHECK (first_contact_address_day BETWEEN 1 AND 10000);

-- Per-number throughput: carrier texts a number may start per minute.
ALTER TABLE msg_addresses
    ADD COLUMN sends_per_minute INT NOT NULL DEFAULT 60 CHECK (sends_per_minute BETWEEN 1 AND 600);

-- Consecutive carrier failures, for the degraded state.
ALTER TABLE msg_channels ADD COLUMN fail_streak INT NOT NULL DEFAULT 0;

-- Who started a conversation and on what consent. consent_note is what the
-- sender attested (free text); the first-contact rate limits count
-- origin = 'outbound' rows.
ALTER TABLE conversations
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'inbound' CHECK (origin IN ('inbound', 'outbound')),
    ADD COLUMN created_by_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN consent_note TEXT NOT NULL DEFAULT '' CHECK (length(consent_note) <= 500);
CREATE INDEX conversations_outbound_recent ON conversations (address_id, created_at) WHERE origin = 'outbound';
CREATE INDEX conversations_outbound_user ON conversations (created_by_user_id, created_at) WHERE origin = 'outbound';

-- The send and status workers' bookkeeping. A text is claimed (queued ->
-- sending, with an attempt id and the claim time) before the carrier is
-- called and is never claimed again. next_poll_at is when its delivery
-- status is next read; NULL once it is final or too old.
ALTER TABLE conversation_messages
    ADD COLUMN attempt_id TEXT,
    ADD COLUMN claimed_at TIMESTAMPTZ,
    ADD COLUMN next_poll_at TIMESTAMPTZ,
    ADD COLUMN poll_count INT NOT NULL DEFAULT 0;
CREATE INDEX conversation_messages_poll ON conversation_messages (next_poll_at) WHERE next_poll_at IS NOT NULL;
CREATE INDEX conversation_messages_claimed ON conversation_messages (claimed_at) WHERE claimed_at IS NOT NULL;

-- +goose Down
DROP INDEX conversation_messages_claimed;
DROP INDEX conversation_messages_poll;
ALTER TABLE conversation_messages
    DROP COLUMN poll_count, DROP COLUMN next_poll_at, DROP COLUMN claimed_at, DROP COLUMN attempt_id;
DROP INDEX conversations_outbound_user;
DROP INDEX conversations_outbound_recent;
ALTER TABLE conversations DROP COLUMN consent_note, DROP COLUMN created_by_user_id, DROP COLUMN origin;
ALTER TABLE msg_channels DROP COLUMN fail_streak;
ALTER TABLE msg_addresses DROP COLUMN sends_per_minute;
ALTER TABLE messaging_settings
    DROP COLUMN first_contact_address_day, DROP COLUMN first_contact_user_hour, DROP COLUMN first_contact;
