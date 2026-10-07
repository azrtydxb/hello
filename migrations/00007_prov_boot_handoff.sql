-- +goose Up
-- When and to which source a phone's boot hand-off was last given, so the
-- same phone's repeated boot requests inside prov.BootHandoffGrace get the
-- same hand-off instead of counting as a reclaim.
ALTER TABLE phones ADD COLUMN boot_handoff_at TIMESTAMPTZ;
ALTER TABLE phones ADD COLUMN boot_handoff_ip INET;

-- +goose Down
ALTER TABLE phones DROP COLUMN boot_handoff_ip;
ALTER TABLE phones DROP COLUMN boot_handoff_at;
