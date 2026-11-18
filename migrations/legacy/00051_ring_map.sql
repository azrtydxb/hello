-- +goose Up
-- Distinctive ringing, part 2 (spec phone-feature-parity S-42 and S-44): the
-- ring map, a tone per ring tag and vendor, written into the phones'
-- provisioned configuration. A row with no group is the global map; a row
-- with a group overrides it for the phones of that group. A tag and vendor
-- with no row use the vendor's built-in default for the tag's Bellcore slot,
-- so nothing has to be configured. tone is the id of a built-in ringer of
-- the vendor's list (the API validates it) or silent. Additive, so a rolling
-- upgrade works.
CREATE TABLE ring_map (
    id         BIGSERIAL   PRIMARY KEY,
    group_id   BIGINT      REFERENCES phone_groups (id) ON DELETE CASCADE,
    tag        TEXT        NOT NULL REFERENCES ring_tags (name) ON UPDATE CASCADE ON DELETE RESTRICT,
    vendor     TEXT        NOT NULL CHECK (vendor IN ('yealink', 'poly', 'grandstream', 'snom', 'fanvil')),
    tone       TEXT        NOT NULL CHECK (tone ~ '^[A-Za-z0-9-]{1,24}$'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ring_map_key ON ring_map (COALESCE(group_id, 0), tag, vendor);
CREATE INDEX ring_map_tag ON ring_map (tag);

-- +goose Down
DROP TABLE ring_map;
