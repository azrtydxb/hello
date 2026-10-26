-- +goose Up
-- The audio codec each leg of a call ended on (name/clock rate, for example
-- PCMU/8000, G722/8000 or opus/48000), as hello-sip negotiated it. NULL for a
-- leg that never had media (a call that was not answered) and for the callee
-- leg of a call Hello ends itself (voicemail, conference, IVR); rows written
-- before this migration stay NULL. Additive, so a rolling upgrade works: an
-- older hello-sip writes the NULL defaults.
ALTER TABLE cdrs ADD COLUMN caller_codec TEXT;
ALTER TABLE cdrs ADD COLUMN callee_codec TEXT;

-- +goose Down
ALTER TABLE cdrs DROP COLUMN callee_codec;
ALTER TABLE cdrs DROP COLUMN caller_codec;
