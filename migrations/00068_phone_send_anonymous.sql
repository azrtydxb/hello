-- +goose Up
-- Phone "Send Anonymous" (Grandstream P65 for account 1). A per extension
-- operator setting, off by default, that provisioning writes to the phone
-- bound to the extension; it is distinct from anonymous_outbound, which only
-- anonymises the legs Hello places through a trunk. Additive, so a rolling
-- upgrade works.
ALTER TABLE extensions ADD COLUMN phone_send_anonymous BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE extensions DROP COLUMN phone_send_anonymous;
