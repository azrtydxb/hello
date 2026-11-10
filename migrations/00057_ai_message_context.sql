-- +goose Up
-- AI on the flow canvas (spec flow-canvas S-19): a question asked from a
-- call-flow canvas carries what the canvas showed (the canvas, the selected
-- nodes, a traced call), stored with the message so the answer's task and
-- the session history see it. NULL for every other message. Additive, so a
-- rolling upgrade works.
ALTER TABLE ai_messages ADD COLUMN context JSONB;

-- +goose Down
ALTER TABLE ai_messages DROP COLUMN context;
