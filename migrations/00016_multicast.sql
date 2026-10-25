-- +goose Up
-- Phase F (phonebooks-ptt spec P-15, phone-feature-parity S-30): a phone
-- group's multicast paging channels and Grandstream PTT settings, and the
-- multicast channel a paging group also sends its page to. Additive, so a
-- rolling upgrade works.

CREATE TABLE multicast_channels (
    id       BIGSERIAL PRIMARY KEY,
    group_id BIGINT    NOT NULL REFERENCES phone_groups (id) ON DELETE CASCADE,
    position INT       NOT NULL CHECK (position >= 0),
    label    TEXT      NOT NULL CHECK (char_length(label) BETWEEN 1 AND 40),
    address  TEXT      NOT NULL,
    port     INT       NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    codec    TEXT      NOT NULL CHECK (codec IN ('PCMU', 'PCMA', 'G722')),
    role     TEXT      NOT NULL CHECK (role IN ('listen', 'send', 'both')),
    priority INT       NOT NULL DEFAULT 1 CHECK (priority BETWEEN 1 AND 25),
    UNIQUE (group_id, position)
);

-- A paging group may also page a multicast channel: the page's audio is
-- sent to the channel's address as well as to the members.
ALTER TABLE paging_groups ADD COLUMN multicast_channel_id BIGINT REFERENCES multicast_channels (id) ON DELETE SET NULL;

-- Push-to-talk of a phone group (Grandstream WP8x6: one multicast address,
-- the channel number selects who hears).
ALTER TABLE phone_groups
    ADD COLUMN ptt_enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN ptt_address         TEXT    NOT NULL DEFAULT '224.0.1.116:5001',
    ADD COLUMN ptt_codec           TEXT    NOT NULL DEFAULT 'PCMU' CHECK (ptt_codec IN ('PCMU', 'G722')),
    ADD COLUMN ptt_default_channel INT     NOT NULL DEFAULT 1 CHECK (ptt_default_channel BETWEEN 1 AND 25),
    ADD COLUMN ptt_accept_busy     BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE ptt_channels (
    group_id  BIGINT  NOT NULL REFERENCES phone_groups (id) ON DELETE CASCADE,
    channel   INT     NOT NULL CHECK (channel BETWEEN 1 AND 25),
    label     TEXT    NOT NULL CHECK (char_length(label) BETWEEN 1 AND 40),
    transmit  BOOLEAN NOT NULL DEFAULT TRUE,
    subscribe BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (group_id, channel)
);

-- +goose Down
DROP TABLE ptt_channels;
ALTER TABLE phone_groups
    DROP COLUMN ptt_accept_busy,
    DROP COLUMN ptt_default_channel,
    DROP COLUMN ptt_codec,
    DROP COLUMN ptt_address,
    DROP COLUMN ptt_enabled;
ALTER TABLE paging_groups DROP COLUMN multicast_channel_id;
DROP TABLE multicast_channels;
