package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// ttsAAD binds the sealed OpenRouter key to its purpose.
const ttsAAD = "tts:openrouter"

// DefaultTTSModel is the model a fresh install starts with.
const DefaultTTSModel = "elevenlabs/eleven-multilingual-v2"

// TTSSettings is the text-to-speech provider configuration as the API
// shows it. The key itself is never part of it: only whether one is set and
// its last four characters.
type TTSSettings struct {
	Provider   string    `json:"provider"`
	Configured bool      `json:"configured"`
	KeyHint    string    `json:"keyHint"`
	Model      string    `json:"model"`
	Voice      string    `json:"voice"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// TTSChange is an update of the settings; nil keeps a field. APIKey is the
// new plain key, sealed before it is stored.
type TTSChange struct {
	APIKey *string
	Model  *string
	Voice  *string
}

// GetTTSSettings returns the provider settings.
func (s *Store) GetTTSSettings(ctx context.Context) (TTSSettings, error) {
	t := TTSSettings{Provider: "openrouter"}
	err := s.db.QueryRowContext(ctx,
		`SELECT api_key_enc IS NOT NULL, key_hint, model, voice, updated_at FROM tts_settings WHERE id = 1`).
		Scan(&t.Configured, &t.KeyHint, &t.Model, &t.Voice, &t.UpdatedAt)
	return t, mapErr(err)
}

// PutTTSSettings changes the settings. A new key is sealed with the secret
// box; without one the change is refused rather than stored in the clear.
func (s *Store) PutTTSSettings(ctx context.Context, actor string, c TTSChange) (TTSSettings, error) {
	var enc []byte
	hint := ""
	if c.APIKey != nil {
		if s.box == nil {
			return TTSSettings{}, errors.New("store: no secret box to seal the TTS key")
		}
		var err error
		if enc, err = s.box.Seal(*c.APIKey, ttsAAD); err != nil {
			return TTSSettings{}, err
		}
		hint = keyHint(*c.APIKey)
	}
	err := s.provChange(ctx, actor, "update", "tts_settings", func(tx *sql.Tx) (string, bool, error) {
		_, err := tx.ExecContext(ctx, `
			UPDATE tts_settings SET
				api_key_enc = COALESCE($1, api_key_enc),
				key_hint    = CASE WHEN $1::bytea IS NULL THEN key_hint ELSE $2 END,
				model       = COALESCE($3, model),
				voice       = COALESCE($4, voice),
				updated_at  = now()
			WHERE id = 1`, enc, hint, c.Model, c.Voice)
		return "tts", false, err
	})
	if err != nil {
		return TTSSettings{}, err
	}
	return s.GetTTSSettings(ctx)
}

// ClearTTSKey removes the stored key.
func (s *Store) ClearTTSKey(ctx context.Context, actor string) (TTSSettings, error) {
	err := s.provChange(ctx, actor, "delete", "tts_key", func(tx *sql.Tx) (string, bool, error) {
		_, err := tx.ExecContext(ctx,
			`UPDATE tts_settings SET api_key_enc = NULL, key_hint = '', updated_at = now() WHERE id = 1`)
		return "tts", false, err
	})
	if err != nil {
		return TTSSettings{}, err
	}
	return s.GetTTSSettings(ctx)
}

// TTSKey returns the plain key for a generation call, or "" when none is
// set. It is the only place the key is opened; callers must not log it.
func (s *Store) TTSKey(ctx context.Context) (string, error) {
	var enc []byte
	if err := s.db.QueryRowContext(ctx, `SELECT api_key_enc FROM tts_settings WHERE id = 1`).Scan(&enc); err != nil {
		return "", mapErr(err)
	}
	if enc == nil {
		return "", nil
	}
	if s.box == nil {
		return "", errors.New("store: no secret box to open the TTS key")
	}
	return s.box.Open(enc, ttsAAD)
}

// keyHint is the last four characters of a key (the whole key is never
// shown, and a key of four or fewer characters shows nothing).
func keyHint(k string) string {
	r := []rune(k)
	if len(r) <= 4 {
		return ""
	}
	return string(r[len(r)-4:])
}

// PromptHash identifies the text, voice and model a prompt's audio is
// generated from. Equal hashes mean generating again would repeat the same
// request.
func PromptHash(text, voice, model string) string {
	h := sha256.New()
	for _, p := range []string{text, voice, model} {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TTSMeta records how a voicemail greeting was generated from text.
type TTSMeta struct {
	Text        string    `json:"text"`
	Voice       string    `json:"voice"`
	Model       string    `json:"model"`
	Hash        string    `json:"hash"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// NewTTSMeta builds the record of a generation that happened now.
func NewTTSMeta(text, voice, model string) *TTSMeta {
	return &TTSMeta{Text: text, Voice: voice, Model: model,
		Hash: PromptHash(text, voice, model), GeneratedAt: time.Now().UTC()}
}

func parseTTSMeta(b []byte) *TTSMeta {
	if len(b) == 0 {
		return nil
	}
	var m TTSMeta
	if json.Unmarshal(b, &m) != nil || m.Hash == "" {
		return nil
	}
	return &m
}

// metaJSON is the JSONB parameter of a record; nil is SQL NULL.
func metaJSON(m *TTSMeta) any {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}
