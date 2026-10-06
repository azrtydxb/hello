-- +goose Up
-- Phone auto-provisioning (spec phone-auto-provisioning-service, Data).
-- Owned by hello-control; hello-sip never reads these tables.

-- A device bound to a phone keeps its secret sealed (internal/secret, AAD
-- "device:<id>") so the renderer can put it in the phone's config; HA1
-- values stay what hello-sip verifies. NULL for every unbound device.
ALTER TABLE devices ADD COLUMN secret_enc BYTEA;

-- Administrator templates. Built-in templates live in the binary
-- (internal/prov/builtin), not here; builtin_ref names the built-in a row
-- copies. An override (phones.template_id) therefore always names a row:
-- to pin a built-in, copy it.
CREATE TABLE prov_templates (
    id          BIGSERIAL   PRIMARY KEY,
    vendor      TEXT        NOT NULL CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil','generic')),
    model_glob  TEXT        NOT NULL CHECK (length(model_glob) BETWEEN 1 AND 64),
    priority    INTEGER     NOT NULL DEFAULT 0,
    name        TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    files       JSONB       NOT NULL CHECK (jsonb_typeof(files) = 'array'), -- [{pattern, contentType, body}]
    builtin_ref TEXT,
    version     INTEGER     NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Previous bodies of a template, one row per saved version.
CREATE TABLE prov_template_versions (
    template_id BIGINT      NOT NULL REFERENCES prov_templates (id) ON DELETE CASCADE,
    version     INTEGER     NOT NULL,
    files       JSONB       NOT NULL CHECK (jsonb_typeof(files) = 'array'),
    actor       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, version)
);

CREATE TABLE phones (
    id                 BIGSERIAL   PRIMARY KEY,
    mac                TEXT        NOT NULL UNIQUE CHECK (mac ~ '^[0-9a-f]{12}$'),
    serial             TEXT        CHECK (length(serial) BETWEEN 1 AND 64),
    vendor             TEXT        NOT NULL CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil','generic')),
    model              TEXT        NOT NULL CHECK (length(model) BETWEEN 1 AND 64),
    label              TEXT        NOT NULL DEFAULT '',
    -- NULL while unbound; an unbound phone is never on the allowlist and
    -- cannot be enabled (the CHECK below).
    device_id          BIGINT      UNIQUE REFERENCES devices (id) ON DELETE RESTRICT,
    -- Override: deleting a template a phone names is refused, so resolution
    -- never changes silently.
    template_id        BIGINT      REFERENCES prov_templates (id) ON DELETE RESTRICT,
    blf                JSONB       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(blf) = 'array'), -- extension numbers, in key order
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    -- Tokens: SHA-256 for lookup, the token sealed (AAD "phone-token:<id>")
    -- to render the phone's own URL. The previous token stays valid until
    -- prev_token_expires or the first fetch with the current one.
    token_hash         BYTEA       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    token_enc          BYTEA       NOT NULL,
    prev_token_hash    BYTEA       UNIQUE CHECK (length(prev_token_hash) = 32),
    prev_token_expires TIMESTAMPTZ,
    token_exposed      BOOLEAN     NOT NULL DEFAULT FALSE,
    ua_mismatch        BOOLEAN     NOT NULL DEFAULT FALSE,
    boot_armed         BOOLEAN     NOT NULL DEFAULT TRUE,
    boot_reclaimed     BOOLEAN     NOT NULL DEFAULT FALSE,
    admin_password_enc BYTEA       NOT NULL, -- AAD "phone-admin:<id>"
    redirect_status    JSONB       NOT NULL DEFAULT '{"state":"not_configured"}' CHECK (jsonb_typeof(redirect_status) = 'object'),
    first_fetch_at     TIMESTAMPTZ,
    last_fetch_at      TIMESTAMPTZ,
    last_fetch_ip      INET,
    last_fetch_ua      TEXT,
    last_fetch_file    TEXT,
    firmware_seen      TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (device_id IS NOT NULL OR NOT enabled),
    CHECK ((prev_token_hash IS NULL) = (prev_token_expires IS NULL))
);

-- Firmware files in MinIO (bucket hello-firmware). A phone requests one by
-- file name under its token, so a name is unique per vendor.
CREATE TABLE prov_firmware (
    id          BIGSERIAL   PRIMARY KEY,
    vendor      TEXT        NOT NULL CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil','generic')),
    model_glob  TEXT        NOT NULL CHECK (length(model_glob) BETWEEN 1 AND 64),
    version     TEXT        NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    filename    TEXT        NOT NULL CHECK (filename ~ '^[A-Za-z0-9_+-][A-Za-z0-9._+-]{0,127}$'),
    object_key  TEXT        NOT NULL UNIQUE, -- <vendor>/<sha256>/<filename>
    size        BIGINT      NOT NULL CHECK (size >= 0),
    sha256      TEXT        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (vendor, filename)
);

-- One pinned firmware per vendor and model glob; a pinned file cannot be
-- deleted, so a rendered firmware URL never points at a missing object.
CREATE TABLE prov_firmware_pins (
    vendor      TEXT   NOT NULL CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil','generic')),
    model_glob  TEXT   NOT NULL CHECK (length(model_glob) BETWEEN 1 AND 64),
    firmware_id BIGINT NOT NULL REFERENCES prov_firmware (id) ON DELETE RESTRICT,
    PRIMARY KEY (vendor, model_glob)
);

-- Vendor redirect-service accounts; credentials sealed with AAD
-- "redirect:<vendor>" and never returned.
CREATE TABLE prov_redirect_accounts (
    vendor            TEXT        PRIMARY KEY CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil')),
    credentials_enc   BYTEA,
    settings          JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(settings) = 'object'),
    enabled           BOOLEAN     NOT NULL DEFAULT FALSE,
    last_check_at     TIMESTAMPTZ,
    last_check_result TEXT
);

-- The redirect worker's queue (plan Task 4). One pending operation per
-- vendor and MAC: a newer one replaces an older one (an unregister after a
-- register wins). It holds no URL and no token: a register reads the
-- phone's current URL when it runs, and an unregister needs only the MAC,
-- so a job outlives the phone it was for. first_queued_at bounds the
-- 24-hour give-up across replacements of the same operation.
CREATE TABLE prov_redirect_jobs (
    vendor          TEXT        NOT NULL CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil')),
    mac             TEXT        NOT NULL CHECK (mac ~ '^[0-9a-f]{12}$'),
    op              TEXT        NOT NULL CHECK (op IN ('register','unregister')),
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    first_queued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (vendor, mac)
);
CREATE INDEX prov_redirect_jobs_due ON prov_redirect_jobs (next_attempt_at);

-- One row per request to the provisioning listener. phone_id has no foreign
-- key: the buffered audit writer may insert after the phone is deleted, and
-- the history outlives the phone until the retention prunes it. The CHECK
-- refuses a path that still carries a token.
CREATE TABLE prov_fetches (
    id            BIGSERIAL   PRIMARY KEY,
    at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    phone_id      BIGINT,
    mac_claimed   TEXT        NOT NULL DEFAULT '',
    ip            INET,
    user_agent    TEXT        NOT NULL DEFAULT '' CHECK (octet_length(user_agent) <= 256),
    path_redacted TEXT        NOT NULL CHECK (path_redacted !~ '^/p/[a-z2-7]{52}(/|$)'),
    kind          TEXT        NOT NULL CHECK (kind IN ('common','device','master','firmware','ca','boot','upload','other')),
    result        TEXT        NOT NULL CHECK (result IN ('served','not_modified','unknown_token','mac_mismatch',
                                                         'not_allowlisted','plain_http','rate_limited','no_template',
                                                         'render_error','boot_served','boot_handoff','boot_reclaim',
                                                         'boot_denied','upload_discarded','not_found','unavailable')),
    status        INTEGER     NOT NULL,
    bytes         BIGINT      NOT NULL DEFAULT 0 CHECK (bytes >= 0)
);
CREATE INDEX prov_fetches_phone_at ON prov_fetches (phone_id, at DESC);
CREATE INDEX prov_fetches_at ON prov_fetches (at);

-- +goose Down
DROP TABLE prov_fetches;
DROP TABLE prov_redirect_jobs;
DROP TABLE prov_redirect_accounts;
DROP TABLE prov_firmware_pins;
DROP TABLE prov_firmware;
DROP TABLE phones;
DROP TABLE prov_template_versions;
DROP TABLE prov_templates;
ALTER TABLE devices DROP COLUMN secret_enc;
