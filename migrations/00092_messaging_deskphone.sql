-- +goose Up
-- Messaging channels, phase 3 (spec messaging-channels M-26): desk-phone
-- texting. A conversation on a number with desk_phone_delivery, assigned to a
-- user who turned deskphone_delivery on, is also delivered as a SIP MESSAGE to
-- the phones of the user's extension (the messages queue row carries the
-- conversation_message_id column phase 1 added). A number may name an owner
-- extension, whose phone can reply into an existing conversation on it.
-- Additive, so a rolling upgrade works: an older node never reads the
-- columns and every default keeps today's behaviour (both flags off).

ALTER TABLE msg_addresses
    ADD COLUMN desk_phone_delivery BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN owner_extension_id  BIGINT  REFERENCES extensions (id) ON DELETE SET NULL;
CREATE INDEX msg_addresses_owner ON msg_addresses (owner_extension_id) WHERE owner_extension_id IS NOT NULL;

ALTER TABLE users ADD COLUMN deskphone_delivery BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE users DROP COLUMN deskphone_delivery;
DROP INDEX msg_addresses_owner;
ALTER TABLE msg_addresses DROP COLUMN owner_extension_id, DROP COLUMN desk_phone_delivery;
