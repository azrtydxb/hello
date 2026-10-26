package api

// Text-to-speech prompts. The operator authors text in the console and asks
// for it to be generated; the audio is made once through OpenRouter (an
// ElevenLabs model), stored as a WAV at the model's own sample rate and played from storage ever
// after. Nothing here runs at call time, and a generation only happens on an
// explicit request: saving text, listing voices and playing never call the
// provider. The OpenRouter key is sealed in the database, write-only through
// the API, and never logged.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/tts"
)

// Synthesizer is the text-to-speech provider; *tts.Client implements it and
// the tests replace it with a fake, so no test touches the network.
type Synthesizer interface {
	// Synthesize returns 16-bit mono WAV audio for text.
	Synthesize(ctx context.Context, key, model, voice, text string) ([]byte, error)
	CheckKey(ctx context.Context, key string) error
	Voices(ctx context.Context, key string) ([]tts.Model, error)
}

// maxIDLen bounds a voice or model identifier.
const maxIDLen = 128

// Settings.

func (s *server) ttsSettings(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTTSSettings(r.Context())
	if err != nil {
		s.internal(w, "read tts settings", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// putTTSSettings is PUT /api/v1/tts/settings: the API key (write-only), the
// default model and the default voice. Every field is optional.
func (s *server) putTTSSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		APIKey *string `json:"apiKey"`
		Model  *string `json:"model"`
		Voice  *string `json:"voice"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	if in.APIKey != nil {
		k := strings.TrimSpace(*in.APIKey)
		if k == "" || len(k) > 512 || strings.ContainsAny(k, " \t\r\n") {
			// Never echo the value.
			f.add("apiKey", "must be the OpenRouter API key: 1-512 characters without spaces")
		}
		in.APIKey = &k
	}
	if in.Model != nil && (*in.Model == "" || !printable(*in.Model, maxIDLen, false)) {
		f.add("model", "must be 1-%d characters without spaces", maxIDLen)
	}
	if in.Voice != nil && !printable(*in.Voice, maxIDLen, false) {
		f.add("voice", "must be at most %d characters without spaces", maxIDLen)
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	if in.APIKey == nil && in.Model == nil && in.Voice == nil {
		badRequest(w, "apiKey, model or voice is required")
		return
	}
	t, err := s.Store.PutTTSSettings(r.Context(), actor(r).String(), store.TTSChange{
		APIKey: in.APIKey, Model: in.Model, Voice: in.Voice,
	})
	if err != nil {
		s.internal(w, "save tts settings", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTTSKey(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.ClearTTSKey(r.Context(), actor(r).String())
	if err != nil {
		s.internal(w, "clear tts key", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// testTTS is POST /api/v1/tts/settings/test: asks OpenRouter whether the
// stored key is accepted (no credits are spent).
func (s *server) testTTS(w http.ResponseWriter, r *http.Request) {
	key, ok := s.ttsKey(w, r)
	if !ok {
		return
	}
	if err := s.TTS.CheckKey(r.Context(), key); err != nil {
		s.ttsFailure(w, r, "test", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ttsVoices is GET /api/v1/tts/voices: the ElevenLabs models OpenRouter
// offers and each model's voices, for the pickers.
func (s *server) ttsVoices(w http.ResponseWriter, r *http.Request) {
	key, err := s.Store.TTSKey(r.Context())
	if err != nil {
		s.internal(w, "read tts key", err)
		return
	}
	ms, err := s.TTS.Voices(r.Context(), key)
	if err != nil {
		s.ttsFailure(w, r, "voices", err)
		return
	}
	if ms == nil {
		ms = []tts.Model{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": ms})
}

// ttsKey reads the stored key; an unset key answers 409 and reports false.
func (s *server) ttsKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key, err := s.Store.TTSKey(r.Context())
	if err != nil {
		s.internal(w, "read tts key", err)
		return "", false
	}
	if key == "" {
		writeError(w, http.StatusConflict, "conflict", tts.ErrNoKey.Error())
		return "", false
	}
	return key, true
}

// ttsFailure answers a provider failure with the provider's reason (it never
// carries the key) and records it in the audit trail without the text.
func (s *server) ttsFailure(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, tts.ErrNoKey):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, tts.ErrEmptyText), errors.Is(err, tts.ErrTooLong):
		badRequest(w, err.Error())
	default:
		s.Log.Warn("tts: provider call failed", "op", what, "error", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

// Prompt generation.

// promptInput is the authored prompt of a generate request.
type promptInput struct {
	Text    string `json:"text"`
	VoiceID string `json:"voiceId"`
	ModelID string `json:"modelId"`
	Force   bool   `json:"force"`
}

// resolve fills the voice and model from the saved defaults and validates the
// text. It answers 400 and reports false on a bad prompt.
func (s *server) resolvePrompt(w http.ResponseWriter, r *http.Request, in *promptInput) bool {
	in.Text = strings.TrimSpace(in.Text)
	var f fieldErrs
	if err := tts.ValidateText(in.Text); err != nil {
		f.add("text", "%s", err.Error())
	}
	if !printable(in.VoiceID, maxIDLen, false) {
		f.add("voiceId", "must be at most %d characters without spaces", maxIDLen)
	}
	if !printable(in.ModelID, maxIDLen, false) {
		f.add("modelId", "must be at most %d characters without spaces", maxIDLen)
	}
	if len(f) > 0 {
		writeFields(w, f)
		return false
	}
	if in.VoiceID == "" || in.ModelID == "" {
		d, err := s.Store.GetTTSSettings(r.Context())
		if err != nil {
			s.internal(w, "read tts settings", err)
			return false
		}
		if in.VoiceID == "" {
			in.VoiceID = d.Voice
		}
		if in.ModelID == "" {
			in.ModelID = d.Model
		}
	}
	return true
}

// synthesize generates the WAV of a prompt, auditing the attempt. The audit
// row names the resource and never the text.
func (s *server) synthesize(w http.ResponseWriter, r *http.Request, resource, id string, in promptInput) ([]byte, bool) {
	key, ok := s.ttsKey(w, r)
	if !ok {
		return nil, false
	}
	start := time.Now()
	wav, err := s.TTS.Synthesize(r.Context(), key, in.ModelID, in.VoiceID, in.Text)
	if err != nil {
		if aerr := s.Store.Audit(r.Context(), actor(r).String(), "generate_failed", resource, id); aerr != nil {
			s.Log.Warn("tts: audit failed generation", "error", aerr)
		}
		s.ttsFailure(w, r, "generate", err)
		return nil, false
	}
	s.Log.Info("tts: generated prompt", "resource", resource, "id", id,
		"model", in.ModelID, "voice", in.VoiceID, "chars", len([]rune(in.Text)),
		"bytes", len(wav), "elapsed", time.Since(start).Round(time.Millisecond))
	return wav, true
}

// generationResult is the answer of the generate routes.
type generationResult struct {
	Announcement store.Announcement `json:"announcement"`
	// Regenerated is false when the stored audio already came from this exact
	// text, voice and model, so the provider was not called.
	Regenerated bool `json:"regenerated"`
}

// createGeneratedAnnouncement is POST /api/v1/announcements/generate: a new
// announcement whose audio is generated from text.
func (s *server) createGeneratedAnnouncement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		promptInput
	}
	if !decode(w, r, &in) {
		return
	}
	if !usernameRe.MatchString(in.Name) {
		var f fieldErrs
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
		writeFields(w, f)
		return
	}
	if !s.resolvePrompt(w, r, &in.promptInput) {
		return
	}
	if _, ok, err := s.Store.AnnouncementByName(r.Context(), in.Name); err != nil {
		s.internal(w, "read announcement", err)
		return
	} else if ok {
		writeError(w, http.StatusConflict, "conflict", "announcement: already exists")
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	wav, ok := s.synthesize(w, r, "announcement", in.Name, in.promptInput)
	if !ok {
		return
	}
	object := fmt.Sprintf("ann/%s.wav", in.Name)
	if err := s.Objects.PutAnnouncement(r.Context(), object, bytes.NewReader(wav), int64(len(wav))); err != nil {
		s.Log.Error("media: store generated announcement", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	a, err := s.Store.CreateGeneratedAnnouncement(r.Context(), actor(r).String(), in.Name, object,
		in.Text, in.VoiceID, in.ModelID, s.check())
	if err != nil {
		if rmErr := s.Objects.RemoveAnnouncement(r.Context(), object); rmErr != nil {
			s.Log.Warn("media: remove refused announcement audio", "error", rmErr)
		}
		s.configError(w, "announcement", err)
		return
	}
	writeJSON(w, http.StatusCreated, generationResult{Announcement: a, Regenerated: true})
}

// saveAnnouncementPrompt is PATCH /api/v1/announcements/{id}: stores the
// authored text, voice and model without generating anything. The console
// then shows the announcement as edited since its last generation.
func (s *server) saveAnnouncementPrompt(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in promptInput
	if !decode(w, r, &in) {
		return
	}
	if !s.resolvePrompt(w, r, &in) {
		return
	}
	a, err := s.Store.SaveAnnouncementPrompt(r.Context(), actor(r).String(), id, in.Text, in.VoiceID, in.ModelID)
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// generateAnnouncement is POST /api/v1/announcements/{id}/generate: generates
// (or regenerates) the announcement's audio from the given text, or from its
// saved text when none is given. It is idempotent per text, voice and model:
// when the stored audio already came from the same hash the provider is not
// called, unless force is set.
func (s *server) generateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in promptInput
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Store.GetAnnouncement(r.Context(), id)
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		in.Text = a.SourceText
		if in.VoiceID == "" {
			in.VoiceID = a.VoiceID
		}
		if in.ModelID == "" {
			in.ModelID = a.ModelID
		}
	}
	if !s.resolvePrompt(w, r, &in) {
		return
	}
	if a.TextHash != "" && a.TextHash == store.PromptHash(in.Text, in.VoiceID, in.ModelID) && !in.Force {
		writeJSON(w, http.StatusOK, generationResult{Announcement: a, Regenerated: false})
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	wav, ok := s.synthesize(w, r, "announcement", a.Name, in)
	if !ok {
		return
	}
	object := fmt.Sprintf("ann/%s.wav", a.Name)
	if err := s.Objects.PutAnnouncement(r.Context(), object, bytes.NewReader(wav), int64(len(wav))); err != nil {
		s.Log.Error("media: store generated announcement", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	updated, err := s.Store.MarkAnnouncementGenerated(r.Context(), actor(r).String(), id, in.Text, in.VoiceID, in.ModelID)
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	writeJSON(w, http.StatusOK, generationResult{Announcement: updated, Regenerated: true})
}

// Voicemail greetings.

// putVoicemailGreeting is PUT /api/v1/extensions/{id}/voicemail/greeting:
// sets a box's greeting or unreachable audio from an existing announcement
// or from text generated once. Uploading a WAV stays on PUT .../voicemail.
func (s *server) putVoicemailGreeting(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Slot           string `json:"slot"`
		Source         string `json:"source"`
		AnnouncementID int64  `json:"announcementId"`
		promptInput
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	if in.Slot != "greeting" && in.Slot != "unreachable" {
		f.add("slot", "must be greeting or unreachable")
	}
	if in.Source != "announcement" && in.Source != "text" {
		f.add("source", "must be announcement or text")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	prev, err := s.Store.GetVoicemailBox(r.Context(), id)
	if err != nil {
		s.configError(w, "voicemail box", err)
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	prevObject, prevMeta := prev.GreetingObject, prev.GreetingTTS
	if in.Slot == "unreachable" {
		prevObject, prevMeta = prev.UnreachableObject, prev.UnreachableTTS
	}

	var (
		wav  []byte
		meta *store.TTSMeta
	)
	if in.Source == "announcement" {
		a, err := s.Store.GetAnnouncement(r.Context(), in.AnnouncementID)
		if err != nil {
			s.configError(w, "announcement", err)
			return
		}
		obj, err := s.Objects.OpenAudio(r.Context(), AnnouncementsBucket, a.Object)
		if err != nil {
			s.Log.Error("voicemail: read announcement audio", "error", err)
			writeError(w, http.StatusBadGateway, "upstream", "object storage read failed; try again shortly")
			return
		}
		wav, err = io.ReadAll(io.LimitReader(obj.Body, maxGreetingBytes+1))
		_ = obj.Body.Close()
		if err != nil || len(wav) == 0 || len(wav) > maxGreetingBytes {
			badRequest(w, "the announcement audio cannot be used as a greeting")
			return
		}
	} else {
		if !s.resolvePrompt(w, r, &in.promptInput) {
			return
		}
		hash := store.PromptHash(in.Text, in.VoiceID, in.ModelID)
		if prevMeta != nil && prevMeta.Hash == hash && prevObject != "" && !in.Force {
			writeJSON(w, http.StatusOK, map[string]any{"box": s.boxView(r.Context(), prev), "regenerated": false})
			return
		}
		var ok bool
		wav, ok = s.synthesize(w, r, "voicemail_box", fmt.Sprint(prev.ID), in.promptInput)
		if !ok {
			return
		}
		meta = store.NewTTSMeta(in.Text, in.VoiceID, in.ModelID)
	}

	object := fmt.Sprintf("box/%d/%s-%d.wav", id, in.Slot, time.Now().UnixNano())
	if err := s.Objects.Put(r.Context(), object, bytes.NewReader(wav), int64(len(wav))); err != nil {
		s.Log.Error("voicemail: store greeting", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	change := store.VoicemailBoxChange{}
	if in.Slot == "greeting" {
		change.Greeting, change.GreetingTTS = &object, meta
	} else {
		change.Unreachable, change.UnreachableTTS = &object, meta
	}
	b, err := s.Store.UpdateVoicemailBox(r.Context(), actor(r).String(), id, change, s.check())
	if err != nil {
		if rmErr := s.Objects.Remove(r.Context(), object); rmErr != nil {
			s.Log.Warn("voicemail: remove refused greeting", "error", rmErr)
		}
		s.configError(w, "voicemail box", err)
		return
	}
	if prevObject != "" && prevObject != object {
		if err := s.Objects.Remove(r.Context(), prevObject); err != nil {
			s.Log.Warn("voicemail: remove old greeting", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"box": s.boxView(r.Context(), b), "regenerated": true})
}

// Park slot announcement.

// parkClips are the announcements the SIP side joins to speak a park slot
// to the parker: the lead-in, then one clip per digit. The names match
// internal/sip (parkSlotPrompt, parkDigitPrompt); while any is missing the
// parker hears the slot as tones.
var parkClips = [][2]string{
	{"park.slot", "Your call is parked in slot"},
	{"park.digit.0", "zero"}, {"park.digit.1", "one"}, {"park.digit.2", "two"},
	{"park.digit.3", "three"}, {"park.digit.4", "four"}, {"park.digit.5", "five"},
	{"park.digit.6", "six"}, {"park.digit.7", "seven"}, {"park.digit.8", "eight"},
	{"park.digit.9", "nine"},
}

// generateParkAnnouncement is POST /api/v1/park-lots/announcement/generate:
// generates the spoken park slot clips once. A clip whose audio already came
// from the same text, voice and model is skipped, and so is one the operator
// uploaded under that name (their own recording wins), unless force is set.
func (s *server) generateParkAnnouncement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VoiceID string `json:"voiceId"`
		ModelID string `json:"modelId"`
		Force   bool   `json:"force"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) { // the body is optional
		return
	}
	if _, ok := s.ttsKey(w, r); !ok {
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	out := struct {
		Announcements []store.Announcement `json:"announcements"`
		Generated     int                  `json:"generated"`
	}{Announcements: []store.Announcement{}}
	for _, clip := range parkClips {
		name := clip[0]
		p := promptInput{Text: clip[1], VoiceID: in.VoiceID, ModelID: in.ModelID}
		if !s.resolvePrompt(w, r, &p) {
			return
		}
		a, exists, err := s.Store.AnnouncementByName(r.Context(), name)
		if err != nil {
			s.internal(w, "read announcement", err)
			return
		}
		if exists && !in.Force && (a.TextHash == "" || a.TextHash == store.PromptHash(p.Text, p.VoiceID, p.ModelID)) {
			out.Announcements = append(out.Announcements, a)
			continue
		}
		wav, ok := s.synthesize(w, r, "announcement", name, p)
		if !ok {
			return
		}
		object := fmt.Sprintf("ann/%s.wav", name)
		if err := s.Objects.PutAnnouncement(r.Context(), object, bytes.NewReader(wav), int64(len(wav))); err != nil {
			s.Log.Error("media: store generated announcement", "error", err)
			writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
			return
		}
		if exists {
			a, err = s.Store.MarkAnnouncementGenerated(r.Context(), actor(r).String(), a.ID, p.Text, p.VoiceID, p.ModelID)
		} else {
			a, err = s.Store.CreateGeneratedAnnouncement(r.Context(), actor(r).String(), name, object,
				p.Text, p.VoiceID, p.ModelID, s.check())
		}
		if err != nil {
			s.configError(w, "announcement", err)
			return
		}
		out.Announcements = append(out.Announcements, a)
		out.Generated++
	}
	writeJSON(w, http.StatusOK, out)
}
