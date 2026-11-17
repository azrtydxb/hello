-- +goose Up
-- User accounts and extension self-service: the user role, several extensions
-- per user, a profile with an email address and voicemail-to-email
-- preferences, system SMTP settings, and the per-recipient notification
-- queue that voicemail email is sent from.

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('user', 'viewer', 'operator', 'admin'));

ALTER TABLE users
    ADD COLUMN display_name         TEXT    NOT NULL DEFAULT '',
    ADD COLUMN email                TEXT    NOT NULL DEFAULT '',
    ADD COLUMN auth_method          TEXT    NOT NULL DEFAULT 'local' CHECK (auth_method IN ('local')),
    ADD COLUMN enabled              BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN vm_email_notify      BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN vm_email_attach      BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN vm_email_after       TEXT    NOT NULL DEFAULT 'none' CHECK (vm_email_after IN ('none', 'mark_read', 'delete'));

-- The extensions a user manages. An extension belongs to at most one user.
-- users.extension_id (migration 00011) stays as the primary one, the
-- extension whose phones receive the user's personal address book.
CREATE TABLE user_extensions (
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    extension_id BIGINT NOT NULL UNIQUE REFERENCES extensions (id) ON DELETE CASCADE,
    position     INT    NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, extension_id)
);
INSERT INTO user_extensions (user_id, extension_id) SELECT id, extension_id FROM users WHERE extension_id IS NOT NULL;

-- System SMTP settings (one row). The password is sealed with
-- HELLO_SECRET_KEY like trunk passwords and is never returned by the API.
CREATE TABLE smtp_settings (
    id              BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    host            TEXT        NOT NULL DEFAULT '',
    port            INT         NOT NULL DEFAULT 587 CHECK (port BETWEEN 1 AND 65535),
    security        TEXT        NOT NULL DEFAULT 'starttls' CHECK (security IN ('none', 'starttls', 'tls')),
    username        TEXT        NOT NULL DEFAULT '',
    password_sealed BYTEA,
    from_address    TEXT        NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per recipient of a voicemail message's email. Nodes claim rows
-- with FOR UPDATE SKIP LOCKED, so exactly one node sends each one; a row
-- whose sender died is reclaimed after its claim goes stale.
CREATE TABLE voicemail_notifications (
    id              BIGSERIAL   PRIMARY KEY,
    message_id      BIGINT      NOT NULL REFERENCES voicemail_messages (id) ON DELETE CASCADE,
    user_id         BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    to_email        TEXT        NOT NULL,
    attach          BOOLEAN     NOT NULL DEFAULT TRUE,
    after_action    TEXT        NOT NULL DEFAULT 'none' CHECK (after_action IN ('none', 'mark_read', 'delete')),
    status          TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at      TIMESTAMPTZ,
    last_error      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX voicemail_notifications_recipient ON voicemail_notifications (message_id, lower(to_email));
CREATE INDEX voicemail_notifications_due ON voicemail_notifications (status, next_attempt_at) WHERE status IN ('pending', 'sending');

-- +goose Down
DROP TABLE voicemail_notifications;
DROP TABLE smtp_settings;
DROP TABLE user_extensions;
ALTER TABLE users
    DROP COLUMN vm_email_after, DROP COLUMN vm_email_attach, DROP COLUMN vm_email_notify,
    DROP COLUMN must_change_password, DROP COLUMN enabled, DROP COLUMN auth_method,
    DROP COLUMN email, DROP COLUMN display_name;
UPDATE users SET role = 'viewer' WHERE role = 'user';
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('viewer', 'operator', 'admin'));
