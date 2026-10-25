package api

// The voicemail voice in the console: previewing the default (spoken)
// greeting, reverting a greeting to it, and pre-generating the static
// prompts when the text-to-speech settings are saved. The calls themselves
// are spoken by hello-sip (internal/sip, cmd/hello-sip); both sides use
// internal/ttscache, so they share one cache: audio is stored in the
// voicemail bucket as tts/<sha256 of model, voice and text>.wav, and a
// preview here is already warm when a caller needs it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/ttscache"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// ttsObjects adapts the voicemail bucket to ttscache.Store.
type ttsObjects struct{ o Objects }

func (t ttsObjects) Get(ctx context.Context, key string) ([]byte, error) { return t.o.Get(ctx, key) }

func (t ttsObjects) Put(ctx context.Context, key string, data []byte) error {
	return t.o.Put(ctx, key, bytes.NewReader(data), int64(len(data)))
}

// newSpeechCache builds the control plane's view of the voice. Its settings
// are re-read every two seconds (and at once after a save), so a preview
// right after changing the voice speaks the new one.
func newSpeechCache(c Config) *ttscache.Cache {
	var st ttscache.Store
	if c.Objects != nil {
		st = ttsObjects{c.Objects}
	}
	return &ttscache.Cache{
		Store: st, Synth: c.TTS, Log: c.Log, SettingsTTL: 2 * time.Second,
		Settings: func(ctx context.Context) (ttscache.Settings, error) {
			t, err := c.Store.GetTTSSettings(ctx)
			if err != nil || !t.Configured {
				return ttscache.Settings{Model: t.Model, Voice: t.Voice}, err
			}
			key, err := c.Store.TTSKey(ctx)
			if err != nil {
				return ttscache.Settings{}, err
			}
			return ttscache.Settings{APIKey: key, Model: t.Model, Voice: t.Voice}, nil
		},
	}
}

// prewarmVoicemail generates every static prompt of the configured language
// that is not stored yet. Dynamic sentences (counts, message headers, a
// greeting naming its owner) are generated on first use instead.
func (s *server) prewarmVoicemail() {
	if s.PromptLanguage == "" {
		return // no language configured: nothing is pre-generated
	}
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	lang := voiceprompt.Parse(s.PromptLanguage)
	var texts []string
	for _, e := range voiceprompt.Static(lang) {
		texts = append(texts, e.Text)
	}
	res, err := s.speech.Prewarm(ctx, texts, 3)
	if err != nil {
		s.Log.Warn("tts: pre-generating the voicemail prompts stopped", "language", string(lang),
			"made", res.Made, "already_stored", res.Cached, "error", err)
		return
	}
	s.Log.Info("tts: voicemail prompts ready", "language", string(lang), "made", res.Made, "already_stored", res.Cached)
}

// previewVoicemailGreeting is POST /api/v1/extensions/{id}/voicemail/greeting/preview:
// the default greeting of the extension's box in one of its variants, spoken
// in the configured voice, as WAV audio. It is the sentence a caller hears
// when the box has no recording of its own (a recorded name aside), so
// previewing also stores it for the calls.
func (s *server) previewVoicemailGreeting(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// The body is optional: no body previews the general greeting.
	var in struct {
		Slot string `json:"slot"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body: "+jsonProblem(err))
		return
	}
	var v voiceprompt.Variant
	switch in.Slot {
	case "greeting", "":
		v = voiceprompt.Unavailable
	case "busy":
		v = voiceprompt.Busy
	case "unreachable":
		v = voiceprompt.Unreachable
	default:
		var f fieldErrs
		f.add("slot", "must be greeting, busy or unreachable")
		writeFields(w, f)
		return
	}
	ext, err := s.Store.GetExtension(r.Context(), id)
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	lang := voiceprompt.Parse(s.PromptLanguage)
	text := voiceprompt.GreetingText(lang, v, voiceprompt.Owner(lang, ext.Name, ext.Number))
	wav, err := s.speech.Speak(r.Context(), text)
	switch {
	case errors.Is(err, ttscache.ErrNotConfigured):
		writeError(w, http.StatusConflict, "conflict", "no OpenRouter API key is configured; set it in Settings, Voice (TTS)")
		return
	case err != nil:
		s.ttsFailure(w, r, "preview", err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.Itoa(len(wav)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wav)
}

// revertVoicemailGreeting is DELETE /api/v1/extensions/{id}/voicemail/greeting/{slot}:
// removes the box's own audio in slot (greeting, busy, unreachable or name),
// so callers hear the default spoken prompt again.
func (s *server) revertVoicemailGreeting(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	slot := r.PathValue("slot")
	if !slices.Contains(greetingSlots, slot) {
		badRequest(w, "slot must be greeting, busy, unreachable or name")
		return
	}
	prev, err := s.Store.GetVoicemailBox(r.Context(), id)
	if err != nil {
		s.configError(w, "voicemail box", err)
		return
	}
	old := slotObject(prev, slot)
	if old == "" {
		writeJSON(w, http.StatusOK, s.boxView(r.Context(), prev)) // already the default
		return
	}
	empty := ""
	change := store.VoicemailBoxChange{}
	switch slot {
	case "greeting":
		change.Greeting = &empty
	case "busy":
		change.Busy = &empty
	case "unreachable":
		change.Unreachable = &empty
	case "name":
		change.Name = &empty
	}
	b, err := s.Store.UpdateVoicemailBox(r.Context(), actor(r).String(), id, change, s.check())
	if err != nil {
		s.configError(w, "voicemail box", err)
		return
	}
	if s.Objects != nil {
		if err := s.Objects.Remove(r.Context(), old); err != nil {
			s.Log.Warn("voicemail: remove reverted greeting", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, s.boxView(r.Context(), b))
}
