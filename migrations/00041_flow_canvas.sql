-- +goose Up
-- Flow canvases (spec flow-canvas S-7). A canvas is a named, shared view of
-- the call flow: it stores only where each node sits, never the
-- configuration itself, so writing layout is not a configuration change (no
-- revision bump, no notify to hello-sip). Additive: no existing table is
-- touched, so a rolling upgrade works.

CREATE TABLE flow_canvases (
    id             BIGSERIAL   PRIMARY KEY,
    name           TEXT        NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    description    TEXT        NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
    -- The saved pan and zoom: {x, y, zoom}.
    viewport       JSONB,
    -- Moves on every layout write; a write made against an older one is
    -- refused so two editors merge per node instead of overwriting.
    layout_version BIGINT      NOT NULL DEFAULT 0,
    created_by     TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE flow_canvas_nodes (
    canvas_id BIGINT  NOT NULL REFERENCES flow_canvases(id) ON DELETE CASCADE,
    -- <kind>:<id|key>. The kind is checked against the node catalogue by
    -- the API, not here, so a new node type needs no migration.
    node_id   TEXT    NOT NULL CHECK (node_id ~ '^[A-Za-z]+:[^[:space:]]{1,200}$'),
    x         REAL    NOT NULL,
    y         REAL    NOT NULL,
    collapsed BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (canvas_id, node_id)
);

-- +goose Down
DROP TABLE flow_canvas_nodes;
DROP TABLE flow_canvases;
