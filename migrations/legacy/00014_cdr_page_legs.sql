-- +goose Up
-- Phase E follow-up (phonebooks-ptt spec P-12): a page's outcome per member
-- is a row of its own, not only a line of the call trace. Additive, so a
-- rolling upgrade works: an older hello-sip never writes the table.
CREATE TABLE cdr_page_legs (
    cdr_id    BIGINT NOT NULL REFERENCES cdrs (id) ON DELETE CASCADE,
    position  INT    NOT NULL,
    extension TEXT   NOT NULL,
    device    TEXT   NOT NULL DEFAULT '',
    result    TEXT   NOT NULL CHECK (result IN ('answered', 'busy', 'dnd', 'no_answer', 'no_registration')),
    PRIMARY KEY (cdr_id, position)
);

-- +goose Down
DROP TABLE cdr_page_legs;
