-- +goose Up
-- IVR / auto-attendant. A menu plays a greeting (an announcement), collects
-- DTMF digits and routes the call to the destination of the chosen option.
-- Prompts are announcements (migration 00005), so any uploaded or generated
-- announcement works. Additive, so a rolling upgrade works.

CREATE TABLE ivr_menus (
    id                  BIGSERIAL   PRIMARY KEY,
    name                TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    -- The number that dials the menu directly; empty means it is only
    -- reachable as a destination (inbound route, ring group failover, a
    -- feature code, another menu's option).
    number              TEXT        NOT NULL DEFAULT '' CHECK (number ~ '^([0-9]{2,16})?$'),
    enabled             BOOLEAN     NOT NULL DEFAULT TRUE,
    greeting_id         BIGINT      NOT NULL REFERENCES announcements (id) ON DELETE RESTRICT,
    invalid_prompt_id   BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    timeout_prompt_id   BIGINT               REFERENCES announcements (id) ON DELETE RESTRICT,
    -- Seconds to wait for the first digit once the greeting has played, and
    -- between the digits of a direct-dialled extension.
    digit_timeout       INTEGER     NOT NULL DEFAULT 5 CHECK (digit_timeout BETWEEN 1 AND 60),
    inter_digit_timeout INTEGER     NOT NULL DEFAULT 3 CHECK (inter_digit_timeout BETWEEN 1 AND 30),
    -- How often the menu repeats after a timeout or an invalid choice before
    -- the matching exit destination applies.
    max_retries         INTEGER     NOT NULL DEFAULT 3 CHECK (max_retries BETWEEN 0 AND 10),
    -- Whether the caller may dial an extension number from the menu.
    allow_direct_dial   BOOLEAN     NOT NULL DEFAULT FALSE,
    timeout_kind        TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (timeout_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external')),
    timeout_target      TEXT        NOT NULL DEFAULT '',
    invalid_kind        TEXT        NOT NULL DEFAULT 'hangup'
        CHECK (invalid_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external')),
    invalid_target      TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ivr_menus_number ON ivr_menus (number) WHERE number <> '';

-- One row per option. Destination kinds: hangup, extension (target: the
-- number), ring_group (the group name), voicemail (the extension number of
-- the box), announcement (the name), paging (the page code), ivr (a menu
-- name) and external (a number routed through outbound routes).
CREATE TABLE ivr_menu_options (
    menu_id     BIGINT NOT NULL REFERENCES ivr_menus (id) ON DELETE CASCADE,
    digit       TEXT   NOT NULL CHECK (digit ~ '^[0-9*#]$'),
    dest_kind   TEXT   NOT NULL
        CHECK (dest_kind IN ('hangup','extension','ring_group','voicemail','announcement','paging','ivr','external')),
    dest_target TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (menu_id, digit)
);

ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
  CHECK (failure_kind IN ('none','voicemail','external','announcement','ivr'));

ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off','ivr'));

ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
  CHECK (destination_kind IN ('extension','external','sip_uri','internal','ivr'));

-- +goose Down
DELETE FROM inbound_routes WHERE destination_kind = 'ivr';
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
  CHECK (destination_kind IN ('extension','external','sip_uri','internal'));

DELETE FROM feature_codes WHERE action = 'ivr';
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off'));

UPDATE ring_groups SET failure_kind = 'none', failure_target = '' WHERE failure_kind = 'ivr';
ALTER TABLE ring_groups DROP CONSTRAINT ring_groups_failure_kind_check;
ALTER TABLE ring_groups ADD CONSTRAINT ring_groups_failure_kind_check
  CHECK (failure_kind IN ('none','voicemail','external','announcement'));

DROP TABLE ivr_menu_options;
DROP TABLE ivr_menus;
