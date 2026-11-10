-- +goose Up
-- Doorbell webhooks (spec phone-feature-parity S-48): a non-SIP doorbell
-- posts a signed request to Hello, which rings the Door group or chimes a
-- paging group. The secret is sealed under HELLO_SECRET_KEY (AAD binds it to
-- the webhook) and shown once. ring_group_id empty rings every Door group.
-- Additive: an older node ignores the table.
CREATE TABLE door_webhooks (
    id                BIGSERIAL   PRIMARY KEY,
    name              TEXT        NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9._ -]{1,64}$'),
    secret_enc        BYTEA       NOT NULL,
    action            TEXT        NOT NULL CHECK (action IN ('ring_door', 'chime_page')),
    ring_group_id     BIGINT      REFERENCES ring_groups (id) ON DELETE RESTRICT,
    paging_group_id   BIGINT      REFERENCES paging_groups (id) ON DELETE RESTRICT,
    announcement_id   BIGINT      REFERENCES announcements (id) ON DELETE SET NULL,
    rate_limit        INTEGER     NOT NULL DEFAULT 11 CHECK (rate_limit BETWEEN 1 AND 600),
    enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
    last_triggered_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (action = 'chime_page' OR paging_group_id IS NULL),
    CHECK (action = 'ring_door' OR ring_group_id IS NULL),
    CHECK (action <> 'chime_page' OR paging_group_id IS NOT NULL)
);

-- +goose Down
DROP TABLE door_webhooks;
