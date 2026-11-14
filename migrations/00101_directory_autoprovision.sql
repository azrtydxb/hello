-- +goose Up
-- Range auto-provisioning for directory connectors (integrations P12, spec
-- I-11): the extension a connector created for a user is marked with that
-- connector, and the number allocated to the user is kept on the user, so a
-- re-run never renumbers it (a deleted extension comes back with the same
-- number). The settings live in directory_connectors.auto_provision, which
-- 00093 reserved. Additive, so a rolling upgrade works: an older node ignores
-- both columns.

ALTER TABLE extensions
    ADD COLUMN provisioned_by BIGINT REFERENCES directory_connectors (id) ON DELETE SET NULL;
CREATE INDEX extensions_provisioned_by ON extensions (provisioned_by) WHERE provisioned_by IS NOT NULL;

ALTER TABLE users
    ADD COLUMN provisioned_number TEXT CHECK (provisioned_number IS NULL OR provisioned_number ~ '^[0-9]{2,10}$');

-- +goose Down
ALTER TABLE users DROP COLUMN provisioned_number;
DROP INDEX extensions_provisioned_by;
ALTER TABLE extensions DROP COLUMN provisioned_by;
