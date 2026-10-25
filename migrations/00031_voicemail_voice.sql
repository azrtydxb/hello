-- +goose Up
-- Voicemail with a guided voice (spec: voicemail uses the configured TTS voice
-- everywhere). A box gets two more custom audio slots next to greeting_object
-- and unreachable_object: busy_object (the greeting for a busy extension) and
-- name_object (the owner's recorded name, played inside the default spoken
-- greeting). An empty slot means "use the default spoken prompt".
-- busy_tts records how a generated busy greeting was made, as greeting_tts
-- does. A message keeps the caller-ID display name the call carried, so the
-- retrieval session can say who it is from. Additive, so a rolling upgrade
-- works: an older hello-sip ignores every new column.
ALTER TABLE voicemail_boxes
    ADD COLUMN busy_object TEXT NOT NULL DEFAULT '',
    ADD COLUMN name_object TEXT NOT NULL DEFAULT '',
    ADD COLUMN busy_tts    JSONB;

ALTER TABLE voicemail_messages
    ADD COLUMN caller_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE voicemail_messages DROP COLUMN caller_name;
ALTER TABLE voicemail_boxes DROP COLUMN busy_tts, DROP COLUMN name_object, DROP COLUMN busy_object;
