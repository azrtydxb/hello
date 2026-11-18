-- +goose Up
-- The canvas assistant may apply its own batch (flow canvas decision of
-- 2026-11-10). applied_via records how an applied proposal was applied:
-- "ai:<session>" when the assistant applied it for the user, NULL when a
-- person did. batch_id is the configuration batch an applied batch
-- proposal ran as, so the console can revert it from the proposal. Both are
-- NULL on every older row. Additive, so a rolling upgrade works.
ALTER TABLE ai_proposals ADD COLUMN applied_via TEXT;
ALTER TABLE ai_proposals ADD COLUMN batch_id UUID REFERENCES config_batches (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE ai_proposals DROP COLUMN batch_id;
ALTER TABLE ai_proposals DROP COLUMN applied_via;
