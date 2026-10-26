-- +goose Up
-- Backup and restore (spec pbx-gaps S-3): one settings row and the run
-- history. The S3 credentials, when the destination is an external S3
-- service, are sealed with HELLO_SECRET_KEY under the AAD "backup:s3" and are
-- never returned by the API.
CREATE TABLE backup_settings (
    id             BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    cron           TEXT        NOT NULL DEFAULT '0 2 * * *',
    kind           TEXT        NOT NULL DEFAULT 'minio' CHECK (kind IN ('minio', 's3')),
    s3_endpoint    TEXT        NOT NULL DEFAULT '',
    s3_bucket      TEXT        NOT NULL DEFAULT '',
    s3_region      TEXT        NOT NULL DEFAULT '',
    s3_path_style  BOOLEAN     NOT NULL DEFAULT FALSE,
    s3_keys_sealed BYTEA,
    keep_count     INT         NOT NULL DEFAULT 14 CHECK (keep_count BETWEEN 1 AND 365),
    notify_email   TEXT        NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE backup_runs (
    id            BIGSERIAL   PRIMARY KEY,
    kind          TEXT        NOT NULL CHECK (kind IN ('scheduled', 'manual')),
    status        TEXT        NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    attempt       INT         NOT NULL DEFAULT 1,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ,
    db_bytes      BIGINT      NOT NULL DEFAULT 0,
    object_count  INT         NOT NULL DEFAULT 0,
    object_bytes  BIGINT      NOT NULL DEFAULT 0,
    archive_bytes BIGINT      NOT NULL DEFAULT 0,
    object_key    TEXT        NOT NULL DEFAULT '',
    error_class   TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX backup_runs_started ON backup_runs (started_at DESC);
CREATE INDEX backup_runs_key ON backup_runs (object_key);

-- +goose Down
DROP TABLE backup_runs;
DROP TABLE backup_settings;
