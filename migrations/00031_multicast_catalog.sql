-- +goose Up
-- Multicast channels become a system-wide catalog (Paging > Multicast
-- channels) that phone groups and paging groups pick from, and push-to-talk
-- works out of the box on a Grandstream WP8x6: PTT is on by default with
-- channel 1 on the catalog's PTT channel (224.0.1.116:5001, Grandstream's
-- own default), and a phone may override the group's PTT channel.
-- Additive, so a rolling upgrade works: the old per-group columns stay
-- (nullable) until a later cleanup.

ALTER TABLE multicast_channels
    ALTER COLUMN group_id DROP NOT NULL,
    ALTER COLUMN position DROP NOT NULL,
    ALTER COLUMN role DROP NOT NULL,
    ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT TRUE,
    -- paging: plain RTP multicast paging (phones listen, paging groups send);
    -- ptt: a Grandstream PTT/group paging address (P22183).
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'paging' CHECK (kind IN ('paging', 'ptt'));

-- Which catalog channels a phone group listens on or sends to. A group
-- with no rows here uses every enabled paging channel.
CREATE TABLE phone_group_multicast (
    group_id   BIGINT NOT NULL REFERENCES phone_groups (id) ON DELETE CASCADE,
    channel_id BIGINT NOT NULL REFERENCES multicast_channels (id) ON DELETE CASCADE,
    position   INT    NOT NULL CHECK (position >= 0),
    role       TEXT   NOT NULL CHECK (role IN ('listen', 'send', 'both')),
    PRIMARY KEY (group_id, channel_id),
    UNIQUE (group_id, position)
);

INSERT INTO phone_group_multicast (group_id, channel_id, position, role)
SELECT group_id, id, position, role FROM multicast_channels WHERE group_id IS NOT NULL;
-- Catalog channels belong to no group: deleting a group must not delete them.
UPDATE multicast_channels SET group_id = NULL, position = NULL, role = NULL WHERE group_id IS NOT NULL;

-- PTT channels in the catalog: each group's own non-default PTT address,
-- then Grandstream's default address.
INSERT INTO multicast_channels (label, address, port, codec, priority, kind)
SELECT DISTINCT ON (ptt_address) 'PTT ' || ptt_address, split_part(ptt_address, ':', 1),
       split_part(ptt_address, ':', 2)::INT, ptt_codec, 1, 'ptt'
FROM phone_groups WHERE ptt_address <> '224.0.1.116:5001' ORDER BY ptt_address, id;

INSERT INTO multicast_channels (label, address, port, codec, priority, kind)
SELECT 'PTT', '224.0.1.116', 5001, 'PCMU', 1, 'ptt'
WHERE NOT EXISTS (SELECT 1 FROM multicast_channels WHERE kind = 'ptt' AND address = '224.0.1.116' AND port = 5001);

-- The PTT channel of a group (NULL: the catalog's first enabled PTT
-- channel) and of a phone (NULL: its group's).
ALTER TABLE phone_groups
    ALTER COLUMN ptt_enabled SET DEFAULT TRUE,
    ADD COLUMN ptt_channel_id BIGINT REFERENCES multicast_channels (id) ON DELETE SET NULL;
ALTER TABLE phones
    ADD COLUMN ptt_channel_id BIGINT REFERENCES multicast_channels (id) ON DELETE SET NULL;

UPDATE phone_groups g SET ptt_channel_id = (
    SELECT c.id FROM multicast_channels c
    WHERE c.kind = 'ptt' AND c.address || ':' || c.port = g.ptt_address ORDER BY c.id LIMIT 1)
WHERE g.ptt_address <> '224.0.1.116:5001';

-- PTT was off by default before; a group that never configured it (no PTT
-- channels) gets it on with channel 1.
UPDATE phone_groups g SET ptt_enabled = TRUE, ptt_default_channel = 1
WHERE NOT g.ptt_enabled AND NOT EXISTS (SELECT 1 FROM ptt_channels c WHERE c.group_id = g.id);
INSERT INTO ptt_channels (group_id, channel, label, transmit, subscribe)
SELECT g.id, 1, 'Channel 1', TRUE, TRUE FROM phone_groups g
WHERE NOT EXISTS (SELECT 1 FROM ptt_channels c WHERE c.group_id = g.id);

-- Group paging (channels 26 to 50) next to PTT (1 to 25), the priority
-- and emergency channels of both, and how the group's Grandstream WP phones
-- take a multicast page and an intercom call. Defaults are Grandstream's,
-- with intercom barging off.
ALTER TABLE ptt_channels DROP CONSTRAINT IF EXISTS ptt_channels_channel_check;
ALTER TABLE ptt_channels ADD CONSTRAINT ptt_channels_channel_check CHECK (channel BETWEEN 1 AND 50);
ALTER TABLE phone_groups
    ADD COLUMN ptt_priority_channel   INT     NOT NULL DEFAULT 24 CHECK (ptt_priority_channel BETWEEN 1 AND 25),
    ADD COLUMN ptt_emergency_channel  INT     NOT NULL DEFAULT 25 CHECK (ptt_emergency_channel BETWEEN 1 AND 25),
    ADD COLUMN group_paging_enabled   BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN group_paging_default   INT     NOT NULL DEFAULT 26 CHECK (group_paging_default IN (26, 27, 49, 50)),
    ADD COLUMN group_paging_priority  INT     NOT NULL DEFAULT 49 CHECK (group_paging_priority BETWEEN 26 AND 50),
    ADD COLUMN group_paging_emergency INT     NOT NULL DEFAULT 50 CHECK (group_paging_emergency BETWEEN 26 AND 50),
    ADD COLUMN paging_allow_in_dnd    BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN paging_barge           INT     NOT NULL DEFAULT 0 CHECK (paging_barge BETWEEN 0 AND 10),
    ADD COLUMN paging_priority_active BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN paging_timeout         INT     NOT NULL DEFAULT 0 CHECK (paging_timeout BETWEEN 0 AND 3600),
    ADD COLUMN intercom_barge         BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE phone_groups
    DROP COLUMN ptt_priority_channel,
    DROP COLUMN ptt_emergency_channel,
    DROP COLUMN group_paging_enabled,
    DROP COLUMN group_paging_default,
    DROP COLUMN group_paging_priority,
    DROP COLUMN group_paging_emergency,
    DROP COLUMN paging_allow_in_dnd,
    DROP COLUMN paging_barge,
    DROP COLUMN paging_priority_active,
    DROP COLUMN paging_timeout,
    DROP COLUMN intercom_barge;
DELETE FROM ptt_channels WHERE channel > 25;
ALTER TABLE ptt_channels DROP CONSTRAINT IF EXISTS ptt_channels_channel_check;
ALTER TABLE ptt_channels ADD CONSTRAINT ptt_channels_channel_check CHECK (channel BETWEEN 1 AND 25);
ALTER TABLE phones DROP COLUMN ptt_channel_id;
ALTER TABLE phone_groups
    DROP COLUMN ptt_channel_id,
    ALTER COLUMN ptt_enabled SET DEFAULT FALSE;
UPDATE multicast_channels c SET group_id = l.group_id, position = l.position, role = l.role
FROM (SELECT DISTINCT ON (channel_id) channel_id, group_id, position, role
      FROM phone_group_multicast ORDER BY channel_id, group_id) l
WHERE c.id = l.channel_id;
DELETE FROM multicast_channels WHERE group_id IS NULL;
DROP TABLE phone_group_multicast;
ALTER TABLE multicast_channels
    DROP COLUMN kind,
    DROP COLUMN enabled,
    ALTER COLUMN role SET NOT NULL,
    ALTER COLUMN position SET NOT NULL,
    ALTER COLUMN group_id SET NOT NULL;
