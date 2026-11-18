-- +goose Up
-- A voicemail box can speak its prompts in its own language (pbx-gaps S-20).
-- NULL means inherit: the deployment's HELLO_VOICEMAIL_LANGUAGE, then English.
-- A value is a language code with a complete table in internal/voiceprompt
-- (en, nl); the API validates it, and a code a later release drops is spoken
-- in English. No CHECK constraint, so adding a language needs no migration.
-- Additive, so a rolling upgrade works: an older hello-sip ignores the column.
ALTER TABLE voicemail_boxes ADD COLUMN prompt_language TEXT;

-- +goose Down
ALTER TABLE voicemail_boxes DROP COLUMN prompt_language;
