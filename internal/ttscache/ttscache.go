// Package ttscache turns prompt text into 8 kHz WAV audio for the call
// plane and remembers it. Audio is stored in the object store under a key
// derived from a hash of (model, voice, text), so a prompt is synthesized
// once per voice for the whole cluster: every node, and the control plane's
// pre-warming, share it. A call asks for a text and gets audio from memory,
// from the store, or - only the first time - from the provider.
//
// A call must never break because speech is unavailable. Speak therefore
// reports an error for every failure (no key, provider down, timeout) and
// the caller falls back to its built-in audio; after a provider failure the
// cache refuses to try again for a short cooldown, so a dead provider costs
// one timeout rather than one per prompt.
//
// The provider key travels only inside Settings, which redacts it from logs,
// and into Synthesizer.Synthesize. Nothing here logs it.
package ttscache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// KeyPrefix is the object-store prefix of cached audio, in the voicemail
// bucket: tts/<sha256 hex>.wav.
const KeyPrefix = "tts/"

// ErrNotConfigured means no key or model is set: speech is off.
var ErrNotConfigured = errors.New("text to speech is not configured")

// ErrCoolingDown means the provider failed a moment ago and is not asked again
// yet.
var ErrCoolingDown = errors.New("text to speech failed recently; not retrying yet")

// Store is the object store holding cached audio. Get must return an error
// for an absent object.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
}

// Synthesizer is the provider; *tts.Client implements it.
type Synthesizer interface {
	Synthesize(ctx context.Context, key, model, voice, text string) ([]byte, error)
}

// Settings is the provider configuration a synthesis needs.
type Settings struct {
	APIKey string
	Model  string
	Voice  string
}

// Configured reports whether speech can be synthesized.
func (s Settings) Configured() bool { return s.APIKey != "" && s.Model != "" }

// LogValue keeps the key out of logs.
func (s Settings) LogValue() slog.Value {
	return slog.GroupValue(slog.String("model", s.Model), slog.String("voice", s.Voice), slog.Bool("key_set", s.APIKey != ""))
}

// String keeps the key out of formatted output.
func (s Settings) String() string {
	return fmt.Sprintf("tts.Settings{model:%q voice:%q key_set:%v}", s.Model, s.Voice, s.APIKey != "")
}

// GoString keeps the key out of %#v output.
func (s Settings) GoString() string { return s.String() }

// SettingsFunc reads the current settings (the database, in production).
type SettingsFunc func(ctx context.Context) (Settings, error)

// Key is the object-store key of the audio for text in a voice of a model:
// tts/<hex sha256 of model, voice and text>.wav. A different voice or model
// is a different key, so changing the voice never plays stale audio.
func Key(model, voice, text string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(voice))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return KeyPrefix + hex.EncodeToString(h.Sum(nil)) + ".wav"
}

// Defaults.
const (
	DefaultTimeout     = 12 * time.Second
	DefaultCooldown    = 30 * time.Second
	DefaultSettingsTTL = 15 * time.Second
	DefaultMemoryBytes = 32 << 20
)

// Cache is safe for concurrent use.
type Cache struct {
	Store    Store
	Synth    Synthesizer
	Settings SettingsFunc
	Log      *slog.Logger
	// Timeout bounds one synthesis (default DefaultTimeout).
	Timeout time.Duration
	// Cooldown is how long to leave the provider alone after a failure
	// (default DefaultCooldown).
	Cooldown time.Duration
	// SettingsTTL is how long settings are reused (default
	// DefaultSettingsTTL); a change takes effect within it.
	SettingsTTL time.Duration
	// MemoryBytes bounds the in-memory copy (default DefaultMemoryBytes).
	MemoryBytes int
	// Now is the clock (tests).
	Now func() time.Time

	mu       sync.Mutex
	set      Settings
	setUntil time.Time
	failTill time.Time
	mem      map[string][]byte
	memBytes int
	flight   map[string]*flight
}

type flight struct {
	done chan struct{}
	wav  []byte
	err  error
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func durOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// settings returns the settings, reading them at most once per SettingsTTL.
func (c *Cache) settings(ctx context.Context) (Settings, error) {
	c.mu.Lock()
	if c.now().Before(c.setUntil) {
		s := c.set
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()
	s, err := c.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	c.mu.Lock()
	c.set, c.setUntil = s, c.now().Add(durOr(c.SettingsTTL, DefaultSettingsTTL))
	c.mu.Unlock()
	return s, nil
}

// Forget drops the remembered settings and failure cooldown, so a settings
// change takes effect at once on this node.
func (c *Cache) Forget() {
	c.mu.Lock()
	c.setUntil, c.failTill = time.Time{}, time.Time{}
	c.mu.Unlock()
}

// Configured reports whether speech is set up (a key and a model).
func (c *Cache) Configured(ctx context.Context) bool {
	s, err := c.settings(ctx)
	return err == nil && s.Configured()
}

// Speak returns the WAV for text, from memory, the object store or the
// provider, in that order. Any error means "play your built-in audio".
func (c *Cache) Speak(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("ttscache: empty text")
	}
	s, err := c.settings(ctx)
	if err != nil {
		return nil, fmt.Errorf("ttscache: settings: %w", err)
	}
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	return c.get(ctx, s, text)
}

// Has reports whether the audio for text is already stored (it does not
// synthesize).
func (c *Cache) Has(ctx context.Context, text string) bool {
	s, err := c.settings(ctx)
	if err != nil || !s.Configured() {
		return false
	}
	k := Key(s.Model, s.Voice, strings.TrimSpace(text))
	if c.fromMem(k) != nil {
		return true
	}
	b, err := c.Store.Get(ctx, k)
	return err == nil && len(b) > 0
}

func (c *Cache) get(ctx context.Context, s Settings, text string) ([]byte, error) {
	k := Key(s.Model, s.Voice, text)
	if b := c.fromMem(k); b != nil {
		return b, nil
	}
	if c.Store != nil {
		if b, err := c.Store.Get(ctx, k); err == nil && len(b) > 0 {
			c.toMem(k, b)
			return b, nil
		}
	}
	// One synthesis per key at a time: a prefetch and the prompt that needs
	// the same text share it.
	c.mu.Lock()
	if f, ok := c.flight[k]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.wav, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.now().Before(c.failTill) {
		c.mu.Unlock()
		return nil, ErrCoolingDown
	}
	if c.flight == nil {
		c.flight = map[string]*flight{}
	}
	f := &flight{done: make(chan struct{})}
	c.flight[k] = f
	c.mu.Unlock()

	// The synthesis outlives the caller that started it only as far as its
	// own timeout: a prompt abandoned by a hung-up call still lands in the
	// cache for the next one.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durOr(c.Timeout, DefaultTimeout))
	f.wav, f.err = c.synthesize(sctx, s, k, text)
	cancel()
	c.mu.Lock()
	delete(c.flight, k)
	c.mu.Unlock()
	close(f.done)
	return f.wav, f.err
}

func (c *Cache) synthesize(ctx context.Context, s Settings, k, text string) ([]byte, error) {
	start := c.now()
	wav, err := c.Synth.Synthesize(ctx, s.APIKey, s.Model, s.Voice, text)
	if err == nil && len(wav) == 0 {
		err = errors.New("empty audio")
	}
	if err != nil {
		c.mu.Lock()
		c.failTill = c.now().Add(durOr(c.Cooldown, DefaultCooldown))
		c.mu.Unlock()
		// The error never carries the key (internal/tts maps provider errors).
		c.log().Warn("tts: synthesis failed; falling back to built-in audio", "settings", s, "chars", len([]rune(text)), "error", err)
		return nil, err
	}
	c.log().Debug("tts: synthesized", "chars", len([]rune(text)), "bytes", len(wav), "took", c.now().Sub(start).Round(time.Millisecond).String())
	c.toMem(k, wav)
	if c.Store != nil {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if perr := c.Store.Put(pctx, k, wav); perr != nil {
			c.log().Warn("tts: could not store synthesized audio", "error", perr)
		}
		cancel()
	}
	return wav, nil
}

func (c *Cache) fromMem(k string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mem[k]
}

func (c *Cache) toMem(k string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	max := c.MemoryBytes
	if max <= 0 {
		max = DefaultMemoryBytes
	}
	if len(b) > max/4 {
		return // one huge prompt must not push out everything else
	}
	if c.mem == nil {
		c.mem = map[string][]byte{}
	}
	if old, ok := c.mem[k]; ok {
		c.memBytes -= len(old)
	}
	for c.memBytes+len(b) > max {
		for ek, ev := range c.mem { // arbitrary victim: cheap, and a miss only costs a store read
			delete(c.mem, ek)
			c.memBytes -= len(ev)
			break
		}
	}
	c.mem[k] = b
	c.memBytes += len(b)
}

// PrewarmResult counts what Prewarm did.
type PrewarmResult struct {
	Made, Cached, Failed int
}

// Prewarm synthesizes every text not already stored, up to parallel at a
// time, and stops at the first provider failure (the rest would fail too).
// It is called when TTS settings are saved, and by an operator's request.
func (c *Cache) Prewarm(ctx context.Context, texts []string, parallel int) (PrewarmResult, error) {
	var res PrewarmResult
	s, err := c.settings(ctx)
	if err != nil {
		return res, err
	}
	if !s.Configured() {
		return res, ErrNotConfigured
	}
	if parallel < 1 {
		parallel = 1
	}
	seen := map[string]bool{}
	var todo []string
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			todo = append(todo, t)
		}
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		sem      = make(chan struct{}, parallel)
	)
	for _, t := range todo {
		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()
			had := c.Has(ctx, t)
			if _, err := c.get(ctx, s, t); err != nil {
				mu.Lock()
				res.Failed++
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			if had {
				res.Cached++
			} else {
				res.Made++
			}
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return res, firstErr
}
