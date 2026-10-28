-- +goose Up
-- Syslog sink (phone-feature-parity S-27): a phone group may point its
-- phones at Hello's syslog receiver (Grandstream P207 server, P208 level).
-- Off by default. Additive, so a rolling upgrade works.
ALTER TABLE phone_groups
    ADD COLUMN syslog_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN syslog_level   TEXT    NOT NULL DEFAULT 'info' CHECK (syslog_level IN ('debug', 'info', 'warning', 'error'));

-- +goose Down
ALTER TABLE phone_groups DROP COLUMN syslog_level, DROP COLUMN syslog_enabled;
