-- +goose Up
-- Blocklist, anonymous reject and country restriction (pbx-gaps S-7, S-8).
-- call_blocklist holds the numbers an operator blocks or allows, globally or
-- for one trunk; a pattern is a phonebook key, with a trailing * on a prefix
-- entry. The trunks gain their own screening columns. Additive: a trunk with
-- the defaults screens nothing, as before.
CREATE TABLE call_blocklist (
    id              BIGSERIAL PRIMARY KEY,
    pattern         TEXT    NOT NULL CHECK (char_length(pattern) BETWEEN 1 AND 40),
    match           TEXT    NOT NULL DEFAULT 'exact' CHECK (match IN ('exact', 'prefix')),
    action          TEXT    NOT NULL DEFAULT 'block' CHECK (action IN ('block', 'allow')),
    trunk_id        BIGINT  REFERENCES trunks (id) ON DELETE CASCADE,
    reason          TEXT    NOT NULL DEFAULT '' CHECK (char_length(reason) <= 120),
    announcement_id BIGINT  REFERENCES announcements (id) ON DELETE SET NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One entry per pattern and scope; a pattern has a single action.
CREATE UNIQUE INDEX call_blocklist_pattern ON call_blocklist (pattern, match, coalesce(trunk_id, 0));
CREATE INDEX call_blocklist_trunk ON call_blocklist (trunk_id) WHERE trunk_id IS NOT NULL;

ALTER TABLE trunks
    ADD COLUMN reject_anonymous      BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN allowed_country_codes TEXT[]  NOT NULL DEFAULT '{}',
    ADD COLUMN unknown_caller_action TEXT    NOT NULL DEFAULT 'block' CHECK (unknown_caller_action IN ('allow', 'block')),
    ADD COLUMN reject_code           INTEGER NOT NULL DEFAULT 603 CHECK (reject_code IN (603, 486));

-- +goose Down
ALTER TABLE trunks
    DROP COLUMN reject_code,
    DROP COLUMN unknown_caller_action,
    DROP COLUMN allowed_country_codes,
    DROP COLUMN reject_anonymous;
DROP TABLE call_blocklist;
