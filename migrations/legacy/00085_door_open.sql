-- +goose Up
-- Door-open code (spec phone-feature-parity S-47). A door station may hold a
-- code of 1 to 8 characters of 0-9*#, sealed under HELLO_SECRET_KEY (AAD
-- binds it to the extension), that Hello sends to the door as RFC 2833 events
-- when the answering user dials *50. hangup_after_open ends the call
-- HELLO_DOOR_HANGUP_SECONDS after the code was sent. Additive: an older
-- hello-sip never reads the table.
CREATE TABLE door_open (
    extension_id      BIGINT      PRIMARY KEY REFERENCES extensions (id) ON DELETE CASCADE,
    code_enc          BYTEA,
    hangup_after_open BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE door_open;
