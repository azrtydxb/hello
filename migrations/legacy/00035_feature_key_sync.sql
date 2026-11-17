-- +goose Up
-- Feature key synchronization: the phone's call-forward-on-no-answer ring
-- count (as-feature-event ringCount). 0 means the system ring timeout.
-- Additive, so a rolling upgrade works.
ALTER TABLE extensions
    ADD COLUMN no_answer_rings INTEGER NOT NULL DEFAULT 0 CHECK (no_answer_rings BETWEEN 0 AND 20);

-- +goose Down
ALTER TABLE extensions DROP COLUMN no_answer_rings;
