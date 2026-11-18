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
	if err != nil || rate != 24000 { // the MP3's own rate is kept, not squashed to 8 kHz
		t.Fatalf("rate=%d err=%v", rate, err)
	}
	if n := len(pcm) / 2; n < 3900*3 || n > 5200*3 { // 0.5 s at 24 kHz plus MP3 frame padding
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
	if _, rate, _ := media.DecodeWAV(w); rate != 24000 {
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
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
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

// TestCheckKeyErrors fails if a refused, exhausted or failing key check is
// not mapped to the operator errors, is retried, or if a server without
// /key is not checked through /models.
func TestCheckKeyErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		want  error
		calls int
	}{
		{"forbidden", http.StatusForbidden, ErrUnauthorized, 1},
		{"credits", http.StatusPaymentRequired, ErrQuota, 1},
		{"rate limited", http.StatusTooManyRequests, ErrQuota, 1},
		{"server error", http.StatusInternalServerError, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
			}))
			defer srv.Close()
			err := (&Client{BaseURL: srv.URL}).CheckKey(context.Background(), testKey)
			if err == nil {
				t.Fatal("no error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if tc.want == nil && !strings.Contains(err.Error(), "500") {
				t.Errorf("err = %v, want the status", err)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("err leaks the key: %v", err)
			}
			if calls != tc.calls {
				t.Errorf("calls = %d, want %d", calls, tc.calls)
			}
		})
	}

	t.Run("no key endpoint", func(t *testing.T) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.URL.Path == "/key" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		defer srv.Close()
		if err := (&Client{BaseURL: srv.URL}).CheckKey(context.Background(), testKey); err != nil {
			t.Fatal(err)
		}
		if strings.Join(paths, ",") != "/key,/models" {
			t.Errorf("paths = %v", paths)
		}
	})
}

// roundTripper counts the requests made through an injected HTTP client.
type roundTripper struct{ n int }

func (rt *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.n++
	return http.DefaultTransport.RoundTrip(r)
}

// TestVoicesNoKey fails if Voices needs a key, sends an Authorization
// header without one, bypasses the injected HTTP client, or keeps a model
// that says it does not produce speech.
func TestVoicesNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			t.Errorf("Authorization = %q", h)
		}
		_, _ = w.Write([]byte(`{"data":[
		 {"id":"elevenlabs/eleven-v3","name":"v3","architecture":{"output_modalities":["speech"]},"supported_voices":["sarah"]},
		 {"id":"elevenlabs/scribe","name":"Scribe","architecture":{"output_modalities":["text"]}}]}`))
	}))
	defer srv.Close()
	rt := &roundTripper{}
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Transport: rt}}
	ms, err := c.Voices(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "elevenlabs/eleven-v3" || ms[0].Name != "v3" || len(ms[0].Voices) != 1 {
		t.Fatalf("models = %+v", ms)
	}
	if rt.n != 1 {
		t.Errorf("injected client used %d times", rt.n)
	}
}

// TestVoicesErrors fails if a refused key, a quota answer or an unreadable
// list is not reported as the operator errors, or if a failure is retried.
func TestVoicesErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want error
		msg  string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, ErrUnauthorized, ""},
		{"rate limited", http.StatusTooManyRequests, `{}`, ErrQuota, ""},
		{"server error", http.StatusBadGateway, `{"error":{"message":"down"}}`, nil, "502"},
		{"garbage", http.StatusOK, `not json`, nil, "could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := (&Client{BaseURL: srv.URL}).Voices(context.Background(), testKey)
			if err == nil {
				t.Fatal("no error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %v, want %q", err, tc.msg)
			}
			if calls != 1 {
				t.Errorf("calls = %d, want 1", calls)
			}
		})
	}
}
