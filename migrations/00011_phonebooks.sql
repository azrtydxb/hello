-- +goose Up
-- Address books, phone groups and the user-to-extension link (spec
-- phonebooks-ptt, phase A: P-1, P-2, P-3, P-21). Owned by hello-control;
-- additive, so a rolling upgrade works. The paging, intercom, caller-name
-- and multicast objects of the same spec arrive with their phases.

-- A book has a kind: the one company book and the one extensions book are
-- seeded below, "named" books are created by an admin, and a "personal"
-- book belongs to one user. The extensions book has a row (so a phone group
-- and the API can name it) but never has entries: it is computed from
-- extensions. version counts changes to the book's entries and is bumped in
-- the writing transaction.
CREATE TABLE phonebooks (
    id            BIGSERIAL   PRIMARY KEY,
    kind          TEXT        NOT NULL CHECK (kind IN ('company', 'named', 'extensions', 'personal')),
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    owner_user_id BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    version       BIGINT      NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((kind = 'personal') = (owner_user_id IS NOT NULL))
);
CREATE UNIQUE INDEX phonebooks_one_company ON phonebooks (kind) WHERE kind = 'company';
CREATE UNIQUE INDEX phonebooks_one_extensions ON phonebooks (kind) WHERE kind = 'extensions';
CREATE UNIQUE INDEX phonebooks_one_personal ON phonebooks (owner_user_id) WHERE kind = 'personal';
CREATE UNIQUE INDEX phonebooks_named_name ON phonebooks (lower(name)) WHERE kind = 'named';

INSERT INTO phonebooks (kind, name) VALUES ('company', 'Company'), ('extensions', 'Extensions');

-- numbers is [{label, number, key}]: the label (work, mobile, home, other),
-- the number as typed and its lookup key (internal/phonebook.Key; empty
-- when the number cannot be resolved).
CREATE TABLE phonebook_entries (
    id         BIGSERIAL   PRIMARY KEY,
    book_id    BIGINT      NOT NULL REFERENCES phonebooks (id) ON DELETE CASCADE,
    first_name TEXT        NOT NULL DEFAULT '' CHECK (char_length(first_name) <= 60),
    last_name  TEXT        NOT NULL DEFAULT '' CHECK (char_length(last_name) <= 60),
    company    TEXT        NOT NULL DEFAULT '' CHECK (char_length(company) <= 60),
    numbers    JSONB       NOT NULL CHECK (jsonb_typeof(numbers) = 'array' AND jsonb_array_length(numbers) BETWEEN 1 AND 3),
    note       TEXT        NOT NULL DEFAULT '' CHECK (char_length(note) <= 255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX phonebook_entries_book_name ON phonebook_entries (book_id, lower(last_name), lower(first_name), id);

-- Hello has no site concept, so a phone group stands in for one: it picks
-- which books a phone is delivered. The extensions and company books are on
-- by default; named books are assigned in order through phone_group_books.
CREATE TABLE phone_groups (
    id                 BIGSERIAL   PRIMARY KEY,
    name               TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description        TEXT        NOT NULL DEFAULT '' CHECK (char_length(description) <= 255),
    include_extensions BOOLEAN     NOT NULL DEFAULT TRUE,
    include_company    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX phone_groups_name ON phone_groups (lower(name));

CREATE TABLE phone_group_books (
    group_id BIGINT  NOT NULL REFERENCES phone_groups (id) ON DELETE CASCADE,
    book_id  BIGINT  NOT NULL REFERENCES phonebooks (id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (group_id, book_id),
    UNIQUE (group_id, position)
);

-- A group a phone names can be deleted: its phones fall back to no group.
ALTER TABLE phones ADD COLUMN group_id BIGINT REFERENCES phone_groups (id) ON DELETE SET NULL;

-- The extension a management user is linked to, set by an admin; it is
-- what ties a personal book to the phones of that extension. At most one
-- user per extension.
ALTER TABLE users ADD COLUMN extension_id BIGINT UNIQUE REFERENCES extensions (id) ON DELETE SET NULL;

-- An extension hidden from the automatic extensions book.
ALTER TABLE extensions ADD COLUMN directory_hidden BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE extensions DROP COLUMN directory_hidden;
ALTER TABLE users DROP COLUMN extension_id;
ALTER TABLE phones DROP COLUMN group_id;
DROP TABLE phone_group_books;
DROP TABLE phone_groups;
DROP TABLE phonebook_entries;
DROP TABLE phonebooks;
