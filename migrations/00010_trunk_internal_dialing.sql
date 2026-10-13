-- +goose Up
-- Internal-number dialing (trunk plan Task 2): the per-trunk policy that
-- gates what a trunk may dial in the extension namespace. The default keeps
-- every existing trunk exactly where it was: mode "off" dial no internal
-- number, so no trunk gains reach by the upgrade.

ALTER TABLE trunks ADD COLUMN internal_dialing JSONB NOT NULL DEFAULT '{"mode":"off"}'
    CHECK (internal_dialing->>'mode' IN ('off', 'patterns', 'all'));

-- +goose Down
ALTER TABLE trunks DROP COLUMN internal_dialing;
