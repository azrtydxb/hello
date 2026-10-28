-- +goose Up
-- Distinctive ringing, part 1 (spec phone-feature-parity S-39 to S-41): ring
-- tags, the ordered rules that choose one, a ring group's own tag, and the
-- tag a call rang with in the CDR. The ring map that turns a tag into a tone
-- per vendor follows in a later migration. Additive, so a rolling upgrade
-- works: an older hello-sip ignores the new tables and columns.

-- A tag is a lowercase name; bellcore_slot is the Bellcore-drN the canonical
-- Alert-Info carries (1 to 5). The seven built-in tags are seeded and cannot
-- be renamed or deleted (the API refuses; the flag records it).
CREATE TABLE ring_tags (
    name         TEXT        PRIMARY KEY CHECK (name ~ '^[a-z0-9-]{1,32}$'),
    builtin      BOOLEAN     NOT NULL DEFAULT FALSE,
    bellcore_slot SMALLINT   NOT NULL DEFAULT 1 CHECK (bellcore_slot BETWEEN 1 AND 5),
    description  TEXT        NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO ring_tags (name, builtin, bellcore_slot, description) VALUES
    ('internal',           TRUE, 1, 'A call from another extension'),
    ('external',           TRUE, 2, 'A call that entered on a trunk'),
    ('ringgroup',          TRUE, 3, 'A call through a ring group'),
    ('page',               TRUE, 1, 'A page or intercom call'),
    ('doorbell',           TRUE, 4, 'A door station call'),
    ('alarm',              TRUE, 5, 'An alarm call'),
    ('emergency-callback', TRUE, 1, 'A callback to an extension that just called an emergency number');

-- The ordered rules (at most 100, enforced by the API). match is a JSON
-- object: callerPattern, trunk, did, callerKind and window, any of which may
-- be absent; all that are present must match. The first rule in position
-- order that matches picks its tag.
CREATE TABLE ring_rules (
    id         BIGSERIAL   PRIMARY KEY,
    position   INTEGER     NOT NULL CHECK (position >= 1),
    name       TEXT        NOT NULL DEFAULT '' CHECK (length(name) <= 64),
    match      JSONB       NOT NULL DEFAULT '{}',
    tag        TEXT        NOT NULL REFERENCES ring_tags (name) ON UPDATE CASCADE ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ring_rules_position ON ring_rules (position, id);

-- A ring group may override the ringgroup tag for calls through it.
ALTER TABLE ring_groups
    ADD COLUMN ring_tag TEXT REFERENCES ring_tags (name) ON UPDATE CASCADE ON DELETE RESTRICT;

-- The tag a call rang with and what chose it (kind:page, rule:3, group,
-- trunk, default); empty for a call that rang no internal device.
ALTER TABLE cdrs
    ADD COLUMN ring_tag  TEXT NOT NULL DEFAULT '',
    ADD COLUMN ring_rule TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE cdrs DROP COLUMN ring_rule, DROP COLUMN ring_tag;
ALTER TABLE ring_groups DROP COLUMN ring_tag;
DROP TABLE ring_rules;
DROP TABLE ring_tags;
