-- +goose Up
-- Integration call events (spec integrations P1, I-35). hello-sip appends
-- events to the Valkey stream hello:events; hello-control stores each one
-- here once (the event id is the dedupe key) so the history API and a
-- resuming event stream can read them. Additive: an older node never
-- touches the table.

CREATE TABLE integration_events (
    id               TEXT        PRIMARY KEY,
    -- The Valkey stream id (milliseconds-sequence): the global order of
    -- events and the cursor a client resumes from.
    cursor_ms        BIGINT      NOT NULL,
    cursor_seq       BIGINT      NOT NULL,
    type             TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    correlation_id   TEXT        NOT NULL DEFAULT '',
    -- The extension the event is about; empty for an event with none.
    extension_number TEXT        NOT NULL DEFAULT '',
    payload          JSONB       NOT NULL,
    ingested_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX integration_events_cursor ON integration_events (cursor_ms, cursor_seq);
CREATE INDEX integration_events_created ON integration_events (created_at);
CREATE INDEX integration_events_extension ON integration_events (extension_number, cursor_ms, cursor_seq);
CREATE INDEX integration_events_correlation ON integration_events (correlation_id);

-- +goose Down
DROP TABLE integration_events;
