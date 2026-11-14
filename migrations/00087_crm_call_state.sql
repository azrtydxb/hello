-- +goose Up
-- A connector that writes a call in several steps (Bitrix24 registers it,
-- then finishes it) keeps what it already did here, so a retry after a
-- failure, even after a hello-control restart, continues with the step that
-- failed instead of writing the call a second time. Empty for every other
-- job. Additive, so a rolling upgrade works.
ALTER TABLE crm_call_logs ADD COLUMN state TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE crm_call_logs DROP COLUMN state;
