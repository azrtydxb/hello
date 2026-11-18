-- +goose Up
-- Two-factor authentication with TOTP (spec pbx-gaps S-11, S-12). The secret
-- is sealed under HELLO_SECRET_KEY and is inactive until totp_enabled_at is
-- set. totp_last_step is the last accepted 30 s step, so a code cannot be
-- replayed. Additive: a user with no secret logs in as before.
ALTER TABLE users
    ADD COLUMN totp_secret_enc  BYTEA,
    ADD COLUMN totp_enabled_at  TIMESTAMPTZ,
    ADD COLUMN totp_last_step   BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN totp_required    BOOLEAN NOT NULL DEFAULT FALSE;

-- Recovery codes are 80-bit random values, so a SHA-256 hash is enough.
CREATE TABLE user_recovery_codes (
    user_id   BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash BYTEA  NOT NULL,
    PRIMARY KEY (user_id, code_hash)
);

-- A password-verified login that still needs its second factor. The token is
-- stored as a hash, lives 5 minutes and is single use.
CREATE TABLE auth_mfa_challenges (
    token_hash BYTEA       PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    attempts   INT         NOT NULL DEFAULT 0
);
CREATE INDEX auth_mfa_challenges_expires ON auth_mfa_challenges (expires_at);

-- Failed second-factor attempts per user, for the 10 per 10 minutes limit.
CREATE TABLE auth_mfa_failures (
    user_id BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX auth_mfa_failures_user ON auth_mfa_failures (user_id, at);

-- Global 2FA policy (singleton row): roles whose users must enrol.
CREATE TABLE auth_settings (
    id                BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    require_2fa_roles TEXT[]  NOT NULL DEFAULT '{}'
);
INSERT INTO auth_settings DEFAULT VALUES;

-- +goose Down
DROP TABLE auth_settings;
DROP TABLE auth_mfa_failures;
DROP TABLE auth_mfa_challenges;
DROP TABLE user_recovery_codes;
ALTER TABLE users
    DROP COLUMN totp_required,
    DROP COLUMN totp_last_step,
    DROP COLUMN totp_enabled_at,
    DROP COLUMN totp_secret_enc;
