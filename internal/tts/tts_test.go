package tts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/media"
)

const testKey = "test-key-not-real"

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/tone24k.mp3")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSynthesize fails if the request does not carry the key, model, voice
// and mp3 format to /audio/speech, or if the answer is not an 8 kHz mono WAV.
func TestSynthesize(t *testing.T) {
	audio := fixture(t)
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	wav, err := c.Synthesize(context.Background(), testKey, "elevenlabs/eleven-multilingual-v2", "george", "Hello there.")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q", auth)
	}
	if got["model"] != "elevenlabs/eleven-multilingual-v2" || got["voice"] != "george" ||
		got["input"] != "Hello there." || got["response_format"] != "mp3" {
		t.Errorf("request = %v", got)
	}
	pcm, rate, err := media.DecodeWAV(wav)
	if err != nil || rate != 8000 {
		t.Fatalf("rate=%d err=%v", rate, err)
	}
	if n := len(pcm) / 2; n < 3900 || n > 5200 { // 0.5 s at 8 kHz plus MP3 frame padding
		t.Errorf("%d samples, want about 4000", n)
	}
}

func TestSynthesizeErrors(t *testing.T) {
	status := 0
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	ctx := context.Background()
	run := func(s int, b string) error {
		status, body = s, b
		_, err := c.Synthesize(ctx, testKey, "elevenlabs/x", "v", "text")
		return err
	}
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"error":{"message":"No auth credentials found"}}`, ErrUnauthorized},
		{402, `{"error":{"message":"Insufficient credits"}}`, ErrQuota},
		{429, `{"error":{"message":"slow down"}}`, ErrQuota},
		{200, ``, ErrEmptyAudio},
		{200, `not an mp3 at all, just text`, ErrBadAudio},
	} {
		if err := run(tc.status, tc.body); !errors.Is(err, tc.want) {
			t.Errorf("status %d body %q: err = %v, want %v", tc.status, tc.body, err, tc.want)
		}
	}
	if err := run(500, `{"error":{"message":"boom"}}`); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("500: %v", err)
	}
	// The key must not appear in any error.
	if err := run(401, ""); err != nil && strings.Contains(err.Error(), testKey) {
		t.Error("error leaks the key")
	}
	if _, err := c.Synthesize(ctx, "", "m", "v", "text"); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
	if _, err := c.Synthesize(ctx, testKey, "m", "v", "  "); !errors.Is(err, ErrEmptyText) {
		t.Errorf("empty text: %v", err)
	}
	if _, err := c.Synthesize(ctx, testKey, "m", "v", strings.Repeat("a", MaxTextChars+1)); !errors.Is(err, ErrTooLong) {
		t.Errorf("long text: %v", err)
	}
	if _, err := c.Synthesize(ctx, testKey, "m", "v", strings.Repeat("é", MaxTextChars)); errors.Is(err, ErrTooLong) {
		t.Error("the cap counts characters, not bytes")
	}
}

func TestPCMToWAV(t *testing.T) {
	if _, err := PCMToWAV(nil, 24000); !errors.Is(err, ErrEmptyAudio) {
		t.Errorf("empty: %v", err)
	}
	if _, err := PCMToWAV([]byte{1, 2, 3}, 24000); !errors.Is(err, ErrBadAudio) {
		t.Errorf("odd: %v", err)
	}
	w, err := PCMToWAV(make([]byte, 48000), 24000)
	if err != nil {
		t.Fatal(err)
	}
	if _, rate, _ := media.DecodeWAV(w); rate != 8000 {
		t.Errorf("rate %d", rate)
	}
}

func TestCheckKey(t *testing.T) {
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" || r.Header.Get("Authorization") != "Bearer "+testKey {
			code = 401
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	if err := c.CheckKey(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckKey(context.Background(), "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong key: %v", err)
	}
	if err := c.CheckKey(context.Background(), ""); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
}

func TestVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.URL.Query().Get("output_modalities") != "speech" {
			t.Errorf("url = %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"data":[
		 {"id":"openai/gpt-4o-mini-tts","name":"OpenAI","supported_voices":["alloy"]},
		 {"id":"elevenlabs/eleven-v3","name":"ElevenLabs: v3","supported_voices":["sarah","george"]},
		 {"id":"elevenlabs/eleven-flash-v2.5","name":"ElevenLabs: Flash"}]}`))
	}))
	defer srv.Close()
	ms, err := (&Client{BaseURL: srv.URL}).Voices(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "elevenlabs/eleven-flash-v2.5" || len(ms[0].Voices) != 0 || len(ms[1].Voices) != 2 {
		t.Fatalf("models = %+v", ms)
	}
}
