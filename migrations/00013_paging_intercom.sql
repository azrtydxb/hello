-- +goose Up
-- Phase E (phonebooks-ptt spec P-12, P-13): server paging groups, 1-to-1
-- intercom and the CDR kind. Additive, so a rolling upgrade works.

CREATE TABLE paging_groups (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    code       TEXT        NOT NULL UNIQUE CHECK (code ~ '^\*[0-9]{2,4}$'),
    priority   TEXT        NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal', 'emergency')),
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Members are the extensions the page rings. Positions only order the list
-- for display; every member is paged at once.
CREATE TABLE paging_group_members (
    group_id     BIGINT NOT NULL REFERENCES paging_groups (id) ON DELETE CASCADE,
    extension_id BIGINT NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    position     INT    NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, extension_id)
);

-- Allowed callers: empty means any internal extension.
CREATE TABLE paging_group_callers (
    group_id     BIGINT NOT NULL REFERENCES paging_groups (id) ON DELETE CASCADE,
    extension_id BIGINT NOT NULL REFERENCES extensions (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, extension_id)
);

-- Whether an intercom call auto-answers on this extension (otherwise it
-- rings normally).
ALTER TABLE extensions ADD COLUMN allow_intercom BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE cdrs ADD COLUMN kind TEXT NOT NULL DEFAULT 'call' CHECK (kind IN ('call', 'page', 'intercom'));

ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom'));

-- Existing installs get the default intercom prefix; a fresh install (empty
-- table) gets it from the control process's defaults instead, so the
-- "seed only an empty table" rule still holds.
INSERT INTO feature_codes (code, action, argument)
SELECT '*80', 'intercom', '' WHERE EXISTS (SELECT 1 FROM feature_codes)
ON CONFLICT (code) DO NOTHING;

-- +goose Down
DELETE FROM feature_codes WHERE action IN ('page', 'intercom');
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement'));
ALTER TABLE cdrs DROP COLUMN kind;
ALTER TABLE extensions DROP COLUMN allow_intercom;
DROP TABLE paging_group_callers;
DROP TABLE paging_group_members;
DROP TABLE paging_groups;
