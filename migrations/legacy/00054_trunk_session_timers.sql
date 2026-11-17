-- +goose Up
-- RFC 4028 session timers on trunk legs (phone-feature-parity S-9, follow-up
-- 9b): per trunk, "on" asks for and keeps a session timer on the trunk's
-- dialogs, "off" never does, and "default" follows Hello's own setting
-- (HELLO_SIP_SESSION_TIMERS_TRUNKS, off unless set). Additive, so a rolling upgrade works: a node that
-- does not know the column ignores it.

ALTER TABLE trunks ADD COLUMN session_timers TEXT NOT NULL DEFAULT 'default'
    CHECK (session_timers IN ('default', 'on', 'off'));

-- +goose Down
ALTER TABLE trunks DROP COLUMN session_timers;
