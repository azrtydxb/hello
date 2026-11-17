-- +goose Up
-- HD voice (wideband codecs): the audio codecs a trunk is offered, in the
-- caller's order. Most carriers accept G.711 only, so the default keeps every
-- existing trunk exactly where it was: PCMU and PCMA, no G.722 or Opus. An
-- operator widens it per trunk; phones are always offered what they offered.
-- Additive, so a rolling upgrade works.

ALTER TABLE trunks ADD COLUMN codecs JSONB NOT NULL DEFAULT '["PCMU","PCMA"]'
    CHECK (jsonb_typeof(codecs) = 'array' AND jsonb_array_length(codecs) BETWEEN 1 AND 4);

-- +goose Down
ALTER TABLE trunks DROP COLUMN codecs;
