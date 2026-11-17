-- +goose Up
-- Phone onboarding queue (spec phone-onboarding): a phone that is not yet a
-- device asks for its configuration by MAC and is recorded here, so an
-- operator can adopt it from the console. Additive, so a rolling upgrade
-- works. The provisioning endpoint is unauthenticated: the table is capped
-- (500 rows, enforced in the capture transaction), rows expire after 30 days
-- unseen, and a MAC already a phone is never recorded.

CREATE TABLE onboarding_candidates (
    mac           TEXT        PRIMARY KEY CHECK (mac ~ '^[0-9a-f]{12}$'),
    vendor        TEXT        NOT NULL DEFAULT 'generic'
                              CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil','generic')),
    model         TEXT        NOT NULL DEFAULT '' CHECK (char_length(model) <= 64),
    firmware      TEXT        NOT NULL DEFAULT '' CHECK (char_length(firmware) <= 64),
    source_ip     INET,
    requested_path TEXT        NOT NULL DEFAULT '' CHECK (char_length(requested_path) <= 256),
    first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_count BIGINT      NOT NULL DEFAULT 1 CHECK (request_count >= 1)
);

CREATE INDEX onboarding_candidates_last_seen ON onboarding_candidates (last_seen);

-- MACs an operator chose to ignore: never recorded again until removed.
CREATE TABLE onboarding_ignored (
    mac        TEXT        PRIMARY KEY CHECK (mac ~ '^[0-9a-f]{12}$'),
    vendor     TEXT        NOT NULL DEFAULT '',
    model      TEXT        NOT NULL DEFAULT '',
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE onboarding_ignored;
DROP TABLE onboarding_candidates;
