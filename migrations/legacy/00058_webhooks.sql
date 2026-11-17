-- +goose Up
-- Outbound webhooks (spec integrations P2, I-42). An endpoint receives the
-- call events that match its filter; every (endpoint, event) pair is one
-- delivery row, queued in the event ingest transaction so an event is never
-- stored without its deliveries. Workers on every control replica claim due
-- rows with FOR UPDATE SKIP LOCKED. Additive: an older node never touches
-- the tables.

CREATE TABLE webhook_endpoints (
    id                      BIGSERIAL   PRIMARY KEY,
    name                    TEXT        NOT NULL,
    url                     TEXT        NOT NULL,
    -- The signing secret, sealed with HELLO_SECRET_KEY under the AAD
    -- "webhook:<id>"; it is shown once, at creation and at rotation.
    secret_enc              BYTEA       NOT NULL,
    -- During the rotation window both secrets sign (one v1= each).
    previous_secret_enc     BYTEA,
    previous_secret_expires TIMESTAMPTZ,
    -- Exact event types or prefix patterns (call.*); empty is every type.
    events                  TEXT[]      NOT NULL DEFAULT '{}',
    -- Only events about these extensions; empty is every extension.
    extensions              TEXT[]      NOT NULL DEFAULT '{}',
    enabled                 BOOLEAN     NOT NULL DEFAULT TRUE,
    -- http and private targets, reachable only inside the administrator's
    -- HELLO_INTEGRATIONS_ALLOWED_CIDRS.
    allow_private           BOOLEAN     NOT NULL DEFAULT FALSE,
    disabled_reason         TEXT        NOT NULL DEFAULT '',
    consecutive_failures    INT         NOT NULL DEFAULT 0,
    last_success_at         TIMESTAMPTZ,
    last_failure_at         TIMESTAMPTZ,
    last_status             INT,
    created_by              TEXT        NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    id              BIGSERIAL   PRIMARY KEY,
    endpoint_id     BIGINT      NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    -- The event (integration_events.id); the payload is read from there, and
    -- the event is kept while a delivery that still needs it exists.
    event_id        TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'retrying', 'succeeded', 'dead')),
    -- attempt counts every try; series_attempt the tries since the last
    -- redelivery, which is what the retry schedule follows.
    attempt         INT         NOT NULL DEFAULT 0,
    series_attempt  INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    -- A worker's lease: a row claimed past this time is taken again, so a
    -- replica that dies mid-delivery loses nothing.
    claimed_until   TIMESTAMPTZ,
    last_status     INT,
    last_error      TEXT        NOT NULL DEFAULT '',
    last_latency_ms INT,
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (endpoint_id, event_id)
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (status, next_attempt_at);
CREATE INDEX webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, id DESC);
CREATE INDEX webhook_deliveries_event ON webhook_deliveries (event_id);

-- The delivery log: one row per attempt.
CREATE TABLE webhook_attempts (
    id           BIGSERIAL   PRIMARY KEY,
    delivery_id  BIGINT      NOT NULL REFERENCES webhook_deliveries (id) ON DELETE CASCADE,
    attempt      INT         NOT NULL,
    -- The Hello-Delivery header of the attempt.
    delivery_ref TEXT        NOT NULL,
    at           TIMESTAMPTZ NOT NULL,
    http_status  INT,
    error        TEXT        NOT NULL DEFAULT '',
    latency_ms   INT         NOT NULL DEFAULT 0
);
CREATE INDEX webhook_attempts_delivery ON webhook_attempts (delivery_id, attempt);

-- +goose Down
DROP TABLE webhook_attempts;
DROP TABLE webhook_deliveries;
DROP TABLE webhook_endpoints;
