-- +goose Up
-- Configuration batches (spec flow-canvas S-14): one row per applied batch,
-- kept for 90 days. ops holds the batch as sent (placeholders resolved, show-once
-- credentials withheld) and results holds, per operation, the object before
-- and after, so a batch can be reverted. Additive: no existing table is
-- touched, so a rolling upgrade works.

CREATE TABLE config_batches (
    id            UUID        PRIMARY KEY,
    -- The user or token that applied it, as the audit trail names it.
    actor         TEXT        NOT NULL,
    -- The outer origin: an OAuth client id, ai-proposal:<id>, ai-assistant,
    -- or empty for the console and API tokens. Each audit row the batch
    -- wrote carries via batch:<id>.
    via           TEXT        NOT NULL DEFAULT '',
    base_revision BIGINT      NOT NULL,
    revision      BIGINT      NOT NULL,
    ops           JSONB       NOT NULL,
    results       JSONB       NOT NULL,
    -- The batch this one undoes.
    reverts       UUID        REFERENCES config_batches (id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX config_batches_created ON config_batches (created_at DESC);
CREATE INDEX config_batches_reverts ON config_batches (reverts) WHERE reverts IS NOT NULL;

-- +goose Down
DROP TABLE config_batches;
