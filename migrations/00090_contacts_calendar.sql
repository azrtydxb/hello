-- +goose Up
-- Outlook and Google contacts lookup and calendar-driven DND (spec
-- integrations P18, I-56). The per-user OAuth connections already exist in
-- crm_connections (scope 'user'); a user chooses per connection whether the
-- calendar may switch DND on, and the poller keeps its own state here.
ALTER TABLE crm_connections
    ADD COLUMN calendar_dnd        BOOLEAN     NOT NULL DEFAULT FALSE,
    -- calendar_active is set while this connection's calendar holds the
    -- user's DND on; only then may the poller switch it off.
    ADD COLUMN calendar_active     BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN calendar_busy_until TIMESTAMPTZ,
    ADD COLUMN calendar_checked_at TIMESTAMPTZ,
    ADD COLUMN calendar_error      TEXT        NOT NULL DEFAULT '';

-- Who set an extension's DND: the user or the calendar poller. A calendar
-- DND is cleared at the end of the event; a manual one never is, and the
-- poller never overrides it.
ALTER TABLE extensions
    ADD COLUMN dnd_source TEXT NOT NULL DEFAULT 'manual' CHECK (dnd_source IN ('manual', 'calendar'));

-- +goose Down
ALTER TABLE extensions DROP COLUMN dnd_source;
ALTER TABLE crm_connections
    DROP COLUMN calendar_error, DROP COLUMN calendar_checked_at, DROP COLUMN calendar_busy_until,
    DROP COLUMN calendar_active, DROP COLUMN calendar_dnd;
