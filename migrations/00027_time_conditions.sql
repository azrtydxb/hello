-- +goose Up
-- Time and date routing (time conditions). A schedule holds the weekly
-- open hours in a time zone; calendars hold holidays and exceptions; a time
-- condition picks a destination from the schedule's verdict (open, closed,
-- holiday) with a day/night override that lives here, so it survives a
-- restart and an HA failover. Additive, so a rolling upgrade works.

CREATE TABLE schedules (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    -- An IANA zone name; the weekly ranges and the calendar dates are wall
    -- clock in this zone, so daylight saving needs no special case.
    time_zone  TEXT        NOT NULL CHECK (time_zone ~ '^[A-Za-z0-9_+/-]{1,64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Open-hours ranges, several per weekday allowed. weekday 0 is Sunday. An
-- end before the start crosses midnight (22:00-06:00 stays open until 06:00
-- the next day); an end of 24:00 is midnight at the end of the day.
CREATE TABLE schedule_ranges (
    schedule_id BIGINT   NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    weekday     SMALLINT NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_time  TEXT     NOT NULL CHECK (start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    end_time    TEXT     NOT NULL CHECK (end_time ~ '^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$'),
    PRIMARY KEY (schedule_id, weekday, start_time),
    CHECK (start_time <> end_time)
);

-- Holiday / exception calendars.
CREATE TABLE calendars (
    id          BIGSERIAL   PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per exception: a single date or a range of dates (end_date is the
-- last day, inclusive), optionally repeating every year (the year of the
-- dates is then 2000 and ignored; a range may wrap the new year). all_day
-- means the whole day; otherwise start_time/end_time bound the part of each
-- day it covers. mode 'closed' closes the covered time; mode 'open' replaces
-- the weekly hours of the covered days with open_hours, a JSON array of
-- {"start":"HH:MM","end":"HH:MM"}.
CREATE TABLE calendar_entries (
    id          BIGSERIAL PRIMARY KEY,
    calendar_id BIGINT    NOT NULL REFERENCES calendars (id) ON DELETE CASCADE,
    label       TEXT      NOT NULL DEFAULT '' CHECK (length(label) <= 120),
    start_date  DATE      NOT NULL,
    end_date    DATE      NOT NULL,
    yearly      BOOLEAN   NOT NULL DEFAULT FALSE,
    all_day     BOOLEAN   NOT NULL DEFAULT TRUE,
    start_time  TEXT      NOT NULL DEFAULT '' CHECK (start_time ~ '^(([01][0-9]|2[0-3]):[0-5][0-9])?$'),
    end_time    TEXT      NOT NULL DEFAULT '' CHECK (end_time ~ '^((([01][0-9]|2[0-3]):[0-5][0-9])|24:00)?$'),
    mode        TEXT      NOT NULL DEFAULT 'closed' CHECK (mode IN ('closed', 'open')),
    open_hours  JSONB     NOT NULL DEFAULT '[]',
    CHECK (yearly OR end_date >= start_date),
    CHECK (all_day OR (start_time <> '' AND end_time <> '' AND start_time <> end_time))
);
CREATE INDEX calendar_entries_calendar ON calendar_entries (calendar_id);

-- The calendars a schedule consults. A calendar a schedule uses cannot be
-- deleted.
CREATE TABLE schedule_calendars (
    schedule_id BIGINT NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    calendar_id BIGINT NOT NULL REFERENCES calendars (id) ON DELETE RESTRICT,
    PRIMARY KEY (schedule_id, calendar_id)
);

-- A routable time condition. Destination kinds: hangup, extension (target:
-- the number), ring_group (the group name), voicemail (the extension number
-- of the box), announcement (the name; *_then optionally continues to the
-- voicemail box of that extension after the announcement), paging (the page
-- code), ivr (a menu name), external (a number routed through outbound
-- routes) and time_condition (another condition's name). holiday_kind ''
-- means the holiday branch uses the closed destination.
CREATE TABLE time_conditions (
    id               BIGSERIAL   PRIMARY KEY,
    name             TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    schedule_id      BIGINT      NOT NULL REFERENCES schedules (id) ON DELETE RESTRICT,
    open_kind        TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (open_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','time_condition')),
    open_target      TEXT        NOT NULL DEFAULT '',
    open_then        TEXT        NOT NULL DEFAULT '' CHECK (open_then ~ '^([0-9]{2,10})?$'),
    closed_kind      TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (closed_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','time_condition')),
    closed_target    TEXT        NOT NULL DEFAULT '',
    closed_then      TEXT        NOT NULL DEFAULT '' CHECK (closed_then ~ '^([0-9]{2,10})?$'),
    holiday_kind     TEXT        NOT NULL DEFAULT ''
        CHECK (holiday_kind IN ('','hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','time_condition')),
    holiday_target   TEXT        NOT NULL DEFAULT '',
    holiday_then     TEXT        NOT NULL DEFAULT '' CHECK (holiday_then ~ '^([0-9]{2,10})?$'),
    -- The day/night toggle: auto follows the schedule, open and closed
    -- force a branch until changed back.
    override         TEXT        NOT NULL DEFAULT 'auto' CHECK (override IN ('auto','open','closed')),
    override_at      TIMESTAMPTZ,
    override_by      TEXT        NOT NULL DEFAULT '',
    -- The digits dialled after the time-condition feature code (*28<n>);
    -- empty means no feature-code toggle.
    toggle_number    TEXT        NOT NULL DEFAULT '' CHECK (toggle_number ~ '^([0-9]{1,4})?$'),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX time_conditions_toggle_number ON time_conditions (toggle_number) WHERE toggle_number <> '';

-- Time conditions are destinations of IVR options and exits, ring group
-- failover and inbound routes. The feature-code actions follow in 00027,
-- which has to come after 00026 (call waiting) re-creates that CHECK.
ALTER TABLE ivr_menus DROP CONSTRAINT ivr_menus_timeout_kind_check;
ALTER TABLE ivr_menus ADD CONSTRAINT ivr_menus_timeout_kind_check
  CHECK (timeout_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','time_condition'));
ALTER TABLE ivr_menus DROP CONSTRAINT ivr_menus_invalid_kind_check;
ALTER TABLE ivr_menus ADD CONSTRAINT ivr_menus_invalid_kind_check
  CHECK (invalid_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','time_condition'));
ALTER TABLE ivr_menu_options DROP CONSTRAINT ivr_menu_options_dest_kind_check;
ALTER TABLE ivr_menu_options ADD CONSTRAINT ivr_menu_options_dest_kind_check
  CHECK (dest_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external','time_condition'));

ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
  CHECK (failure_kind IN ('none','voicemail','external','announcement','ivr','time_condition'));

ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
  CHECK (destination_kind IN ('extension','external','sip_uri','internal','ivr','time_condition'));

-- +goose Down
DELETE FROM inbound_routes WHERE destination_kind = 'time_condition';
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
  CHECK (destination_kind IN ('extension','external','sip_uri','internal','ivr'));

UPDATE ring_groups SET failure_kind = 'none', failure_target = '' WHERE failure_kind = 'time_condition';
ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
  CHECK (failure_kind IN ('none','voicemail','external','announcement','ivr'));

DELETE FROM ivr_menu_options WHERE dest_kind = 'time_condition';
ALTER TABLE ivr_menu_options DROP CONSTRAINT ivr_menu_options_dest_kind_check;
ALTER TABLE ivr_menu_options ADD CONSTRAINT ivr_menu_options_dest_kind_check
  CHECK (dest_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external'));
UPDATE ivr_menus SET invalid_kind = 'hangup', invalid_target = '' WHERE invalid_kind = 'time_condition';
ALTER TABLE ivr_menus DROP CONSTRAINT ivr_menus_invalid_kind_check;
ALTER TABLE ivr_menus ADD CONSTRAINT ivr_menus_invalid_kind_check
  CHECK (invalid_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external'));
UPDATE ivr_menus SET timeout_kind = 'hangup', timeout_target = '' WHERE timeout_kind = 'time_condition';
ALTER TABLE ivr_menus DROP CONSTRAINT ivr_menus_timeout_kind_check;
ALTER TABLE ivr_menus ADD CONSTRAINT ivr_menus_timeout_kind_check
  CHECK (timeout_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external'));

DROP TABLE time_conditions;
DROP TABLE schedule_calendars;
DROP TABLE calendar_entries;
DROP TABLE calendars;
DROP TABLE schedule_ranges;
DROP TABLE schedules;
