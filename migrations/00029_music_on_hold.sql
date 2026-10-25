-- +goose Up
-- Music on hold (phone-feature-parity S-33): named MOH classes (ordered
-- playlists of announcements, played in a loop or shuffled), one global
-- default class, and the class a call hears per extension (what callers hear
-- while this extension holds them), ring group and inbound route. A ring
-- group or inbound route may also play its class instead of ringback while
-- the destination rings. Additive, so a rolling upgrade works: an older
-- node ignores the new columns and tables.

CREATE TABLE moh_classes (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    mode       TEXT        NOT NULL DEFAULT 'loop' CHECK (mode IN ('loop', 'shuffle')),
    is_default BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one class is the global default.
CREATE UNIQUE INDEX moh_classes_one_default ON moh_classes ((TRUE)) WHERE is_default;

CREATE TABLE moh_class_tracks (
    class_id        BIGINT NOT NULL REFERENCES moh_classes (id) ON DELETE CASCADE,
    position        INT    NOT NULL CHECK (position >= 0),
    announcement_id BIGINT NOT NULL REFERENCES announcements (id) ON DELETE CASCADE,
    PRIMARY KEY (class_id, position)
);

-- A deleted class falls back to the next level (route, group, global
-- default, built-in tone).
ALTER TABLE extensions
    ADD COLUMN moh_class_id BIGINT REFERENCES moh_classes (id) ON DELETE SET NULL;

ALTER TABLE ring_groups
    ADD COLUMN moh_class_id BIGINT  REFERENCES moh_classes (id) ON DELETE SET NULL,
    ADD COLUMN moh_ringback BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE inbound_routes
    ADD COLUMN moh_class_id BIGINT  REFERENCES moh_classes (id) ON DELETE SET NULL,
    ADD COLUMN moh_ringback BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE inbound_routes DROP COLUMN moh_ringback, DROP COLUMN moh_class_id;
ALTER TABLE ring_groups DROP COLUMN moh_ringback, DROP COLUMN moh_class_id;
ALTER TABLE extensions DROP COLUMN moh_class_id;
DROP TABLE moh_class_tracks;
DROP TABLE moh_classes;
