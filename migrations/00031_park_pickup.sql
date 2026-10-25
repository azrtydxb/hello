-- +goose Up
-- Call park and call pickup (phone-feature-parity spec S-6, S-50): park lots,
-- pickup groups and the four feature-code actions that drive them. Additive,
-- so a rolling upgrade works.

-- A park lot is a range of slot numbers a call can be parked in. A slot is
-- dialled like an extension (a BLF key on it shows occupancy), so a lot's
-- range never overlaps an extension number or another lot (checked by the
-- API, which owns the whole dial space).
CREATE TABLE park_lots (
    id            BIGSERIAL   PRIMARY KEY,
    name          TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    slot_start    INT         NOT NULL CHECK (slot_start BETWEEN 1 AND 99999),
    slot_end      INT         NOT NULL CHECK (slot_end BETWEEN 1 AND 99999),
    -- Seconds a parked call waits before it rings the parker again, then
    -- (the ring window) before it goes to the parker's voicemail, or is ended
    -- when the parker has none.
    return_secs   INT         NOT NULL DEFAULT 120 CHECK (return_secs BETWEEN 10 AND 3600),
    voicemail_secs INT        NOT NULL DEFAULT 60 CHECK (voicemail_secs BETWEEN 10 AND 3600),
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (slot_end >= slot_start AND slot_end - slot_start < 100)
);

-- A pickup group: members may answer calls ringing any other member.
CREATE TABLE pickup_groups (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pickup_group_members (
    group_id     BIGINT NOT NULL REFERENCES pickup_groups (id) ON DELETE CASCADE,
    extension_id BIGINT NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, extension_id)
);

CREATE INDEX pickup_group_members_ext ON pickup_group_members (extension_id);

ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'park','park_retrieve','pickup_group','pickup_directed','call_waiting_on','call_waiting_off'));

-- Existing installs get the default codes (park *74, retrieve *75, group
-- pickup *40, directed pickup *41<ext>) unless one would overlap a feature
-- code or paging code already there: an install that put a paging group on
-- *60 or *74 keeps it, and the administrator picks other codes. A fresh
-- install (empty table) gets them from the control process's defaults
-- instead, so the "seed only an empty table" rule still holds.
INSERT INTO feature_codes (code, action, argument)
SELECT v.code, v.action, ''
FROM (VALUES ('*74', 'park'), ('*75', 'park_retrieve'), ('*40', 'pickup_group'), ('*41', 'pickup_directed')) AS v (code, action)
WHERE EXISTS (SELECT 1 FROM feature_codes)
  AND NOT EXISTS (SELECT 1 FROM feature_codes f WHERE starts_with(f.code, v.code) OR starts_with(v.code, f.code))
  AND NOT EXISTS (SELECT 1 FROM paging_groups p WHERE starts_with(p.code, v.code) OR starts_with(v.code, p.code))
ON CONFLICT (code) DO NOTHING;

-- The default lot, slots 701 to 720, unless an extension already uses a
-- number in that range (then the administrator creates a lot elsewhere).
INSERT INTO park_lots (name, slot_start, slot_end)
SELECT 'default', 701, 720
WHERE NOT EXISTS (SELECT 1 FROM extensions WHERE length(number) = 3 AND number BETWEEN '701' AND '720');

-- +goose Down
DELETE FROM feature_codes WHERE action IN ('park', 'park_retrieve', 'pickup_group', 'pickup_directed');
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom','call_waiting_on','call_waiting_off'));
DROP TABLE pickup_group_members;
DROP TABLE pickup_groups;
DROP TABLE park_lots;
