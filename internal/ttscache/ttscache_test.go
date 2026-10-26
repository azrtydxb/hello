package ttscache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
	puts int
}

func (m *memStore) Get(_ context.Context, k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.objs[k]; ok {
		return b, nil
	}
	return nil, errors.New("no such object")
}

func (m *memStore) Put(_ context.Context, k string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objs == nil {
		m.objs = map[string][]byte{}
	}
	m.objs[k] = b
	m.puts++
	return nil
}

// fakeSynth is the stand-in provider: no network, no key.
type fakeSynth struct {
	calls atomic.Int64
	fail  atomic.Bool
	delay time.Duration
	keys  []string
	mu    sync.Mutex
}

func (f *fakeSynth) Synthesize(_ context.Context, key, model, voice, text string) ([]byte, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.keys = append(f.keys, key)
	f.mu.Unlock()
	time.Sleep(f.delay)
	if f.fail.Load() {
		return nil, errors.New("provider down")
	}
	return []byte("WAV:" + model + ":" + voice + ":" + text), nil
}

const testKey = "test-key-not-a-real-secret"

func newCache(syn *fakeSynth, st *memStore, log *slog.Logger) *Cache {
	return &Cache{
		Store: st, Synth: syn, Log: log,
		Settings: func(context.Context) (Settings, error) {
			return Settings{APIKey: testKey, Model: "m1", Voice: "v1"}, nil
		},
	}
}

func TestKeyIsHashOfModelVoiceText(t *testing.T) {
	a := Key("m", "v", "hello")
	if !strings.HasPrefix(a, "tts/hd/") || !strings.HasSuffix(a, ".wav") || len(a) != len("tts/hd/")+64+len(".wav") {
		t.Fatalf("Key = %q", a)
	}
	if a != Key("m", "v", "hello") {
		t.Error("Key is not deterministic")
	}
	for name, other := range map[string]string{
		"text":  Key("m", "v", "hello!"),
		"voice": Key("m", "v2", "hello"),
		"model": Key("m2", "v", "hello"),
		// The fields are separated, so moving a character across a boundary differs.
		"boundary": Key("mv", "", "hello"),
	} {
		if other == a {
			t.Errorf("a different %s gives the same key", name)
		}
	}
}

func TestSpeakCachesInMemoryAndStore(t *testing.T) {
	syn, st := &fakeSynth{}, &memStore{}
	c := newCache(syn, st, nil)
	ctx := context.Background()
	w1, err := c.Speak(ctx, "  Please enter your PIN  ")
	if err != nil {
		t.Fatal(err)
	}
	if syn.calls.Load() != 1 || st.puts != 1 {
		t.Fatalf("calls=%d puts=%d, want 1/1", syn.calls.Load(), st.puts)
	}
	if _, ok := st.objs[Key("m1", "v1", "Please enter your PIN")]; !ok {
		t.Fatalf("not stored under the hash key: %v", st.objs)
	}
	if w2, _ := c.Speak(ctx, "Please enter your PIN"); !bytes.Equal(w1, w2) || syn.calls.Load() != 1 {
		t.Fatalf("second speak resynthesized (calls=%d)", syn.calls.Load())
	}
	// Another node: empty memory, same store. No synthesis.
	c2 := newCache(syn, st, nil)
	if _, err := c2.Speak(ctx, "Please enter your PIN"); err != nil || syn.calls.Load() != 1 {
		t.Fatalf("a second node synthesized again: err=%v calls=%d", err, syn.calls.Load())
	}
	// A voice change is a new key.
	c2.Settings = func(context.Context) (Settings, error) {
		return Settings{APIKey: testKey, Model: "m1", Voice: "v2"}, nil
	}
	c2.Forget()
	if _, err := c2.Speak(ctx, "Please enter your PIN"); err != nil || syn.calls.Load() != 2 {
		t.Fatalf("new voice reused old audio: err=%v calls=%d", err, syn.calls.Load())
	}
}

func TestSpeakSingleFlight(t *testing.T) {
	syn := &fakeSynth{delay: 50 * time.Millisecond}
	c := newCache(syn, &memStore{}, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Speak(context.Background(), "same text"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := syn.calls.Load(); n != 1 {
		t.Fatalf("%d concurrent speaks made %d syntheses, want 1", 8, n)
	}
}

func TestNotConfigured(t *testing.T) {
	syn := &fakeSynth{}
	c := newCache(syn, &memStore{}, nil)
	c.Settings = func(context.Context) (Settings, error) { return Settings{Model: "m"}, nil }
	if _, err := c.Speak(context.Background(), "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if syn.calls.Load() != 0 {
		t.Fatal("synthesized without a key")
	}
	if c.Configured(context.Background()) {
		t.Fatal("Configured with no key")
	}
	c.Settings = func(context.Context) (Settings, error) { return Settings{}, errors.New("db down") }
	c.Forget()
	if _, err := c.Speak(context.Background(), "x"); err == nil {
		t.Fatal("settings failure not reported")
	}
}

// TestFallbackAndCooldown fails if a provider failure is not reported (so the
// caller can fall back), is retried per prompt instead of cooling down, or the
// log line leaks the key.
func TestFallbackAndCooldown(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	syn := &fakeSynth{}
	syn.fail.Store(true)
	now := time.Now()
	c := newCache(syn, &memStore{}, log)
	c.Now = func() time.Time { return now }
	if _, err := c.Speak(context.Background(), "one"); err == nil {
		t.Fatal("provider failure not reported")
	}
	if _, err := c.Speak(context.Background(), "two"); !errors.Is(err, ErrCoolingDown) {
		t.Fatalf("err = %v, want ErrCoolingDown", err)
	}
	if syn.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (cooldown)", syn.calls.Load())
	}
	// Already-cached audio still plays during the cooldown.
	// After the cooldown the provider is tried again and recovers.
	syn.fail.Store(false)
	now = now.Add(time.Minute)
	if _, err := c.Speak(context.Background(), "two"); err != nil {
		t.Fatalf("after cooldown: %v", err)
	}
	if !strings.Contains(logs.String(), "falling back") {
		t.Error("no warning logged")
	}
	if strings.Contains(logs.String(), testKey) {
		t.Fatalf("the key was logged: %s", logs.String())
	}
}

func TestSettingsRedactsKey(t *testing.T) {
	s := Settings{APIKey: testKey, Model: "m", Voice: "v"}
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s)} {
		if strings.Contains(out, testKey) {
			t.Fatalf("formatted settings leak the key: %s", out)
		}
	}
	var b bytes.Buffer
	slog.New(slog.NewJSONHandler(&b, nil)).Info("x", "s", s)
	if strings.Contains(b.String(), testKey) {
		t.Fatalf("slog leaks the key: %s", b.String())
	}
}

func TestPrewarm(t *testing.T) {
	syn, st := &fakeSynth{}, &memStore{}
	c := newCache(syn, st, nil)
	ctx := context.Background()
	if _, err := c.Speak(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	res, err := c.Prewarm(ctx, []string{"a", "b", "c", "b", " ", "c "}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Made != 2 || res.Cached != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want made 2 cached 1", res)
	}
	if syn.calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (a, b, c once each)", syn.calls.Load())
	}
	// Running it again costs nothing.
	res, err = c.Prewarm(ctx, []string{"a", "b", "c"}, 3)
	if err != nil || res.Made != 0 || res.Cached != 3 || syn.calls.Load() != 3 {
		t.Fatalf("second prewarm: %+v %v calls=%d", res, err, syn.calls.Load())
	}
	syn.fail.Store(true)
	if _, err := c.Prewarm(ctx, []string{"d", "e"}, 1); err == nil {
		t.Fatal("prewarm hid a provider failure")
	}
	c.Settings = func(context.Context) (Settings, error) { return Settings{}, nil }
	c.Forget()
	if _, err := c.Prewarm(ctx, []string{"x"}, 1); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryBound(t *testing.T) {
	c := newCache(&fakeSynth{}, &memStore{}, nil)
	c.MemoryBytes = 100
	for i := range 20 {
		c.toMem(fmt.Sprint(i), make([]byte, 20))
	}
	if c.memBytes > 100 {
		t.Fatalf("memBytes = %d, over the bound", c.memBytes)
	}
	c.toMem("huge", make([]byte, 80))
	if _, ok := c.mem["huge"]; ok {
		t.Fatal("an oversized entry was kept")
	}
}
