-- +goose Up
-- Text-to-speech prompts: the operator authors text in the console and asks for
-- it to be generated once; the WAV is stored and replayed from storage. The
-- OpenRouter API key is sealed (AES-256-GCM under HELLO_SECRET_KEY) and never
-- leaves the control plane. Additive, so a rolling upgrade works: an older
-- hello-sip ignores every new column.

CREATE TABLE tts_settings (
    id          SMALLINT    PRIMARY KEY CHECK (id = 1),
    api_key_enc BYTEA,
    key_hint    TEXT        NOT NULL DEFAULT '',
    model       TEXT        NOT NULL DEFAULT 'elevenlabs/eleven-multilingual-v2',
    voice       TEXT        NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO tts_settings (id) VALUES (1);

-- source_text, voice_id and model_id are the authored prompt; text_hash is the
-- hash of the text, voice and model the stored audio was generated from, so a
-- differing hash means the text was edited since.
ALTER TABLE announcements
    ADD COLUMN source_text  TEXT,
    ADD COLUMN voice_id     TEXT,
    ADD COLUMN model_id     TEXT,
    ADD COLUMN generated_at TIMESTAMPTZ,
    ADD COLUMN text_hash    TEXT;

-- greeting_tts and unreachable_tts record how a generated greeting was made
-- ({"text","voice","model","hash","generatedAt"}); NULL for uploaded audio.
ALTER TABLE voicemail_boxes
    ADD COLUMN greeting_tts    JSONB,
    ADD COLUMN unreachable_tts JSONB;

-- +goose Down
ALTER TABLE voicemail_boxes DROP COLUMN unreachable_tts, DROP COLUMN greeting_tts;
ALTER TABLE announcements
    DROP COLUMN text_hash, DROP COLUMN generated_at, DROP COLUMN model_id,
    DROP COLUMN voice_id, DROP COLUMN source_text;
DROP TABLE tts_settings;
