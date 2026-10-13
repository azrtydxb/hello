-- +goose Up
-- Internal-number dialing (trunk plan Task 2): the per-trunk policy that
-- gates what a trunk may dial in the extension namespace. The default keeps
-- every existing trunk exactly where it was: mode "off" dial no internal
-- number, so no trunk gains reach by the upgrade.

ALTER TABLE trunks ADD COLUMN internal_dialing JSONB NOT NULL DEFAULT '{"mode":"off"}'
    CHECK (internal_dialing->>'mode' IN ('off', 'patterns', 'all'));

-- The inbound route table's kind CHECK gains "internal": an inbound route
-- may name an internal number as its destination kind (Task 1's engine).
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
    CHECK (destination_kind IN ('extension', 'external', 'sip_uri', 'internal'));

-- +goose Down
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_destination_kind_check;
ALTER TABLE inbound_routes ADD CONSTRAINT inbound_routes_destination_kind_check
    CHECK (destination_kind IN ('extension', 'external', 'sip_uri'));
ALTER TABLE trunks DROP COLUMN internal_dialing;
