// Package tts turns prompt text into the 16-bit mono WAV the call plane
// plays (at the speech model's own sample rate, up to 48 kHz; playback
// resamples to each call's rate). It reaches ElevenLabs through OpenRouter's OpenAI-compatible
// /audio/speech endpoint, using go-ai-sdk's openaicompatible provider. The
// audio is generated once, on an operator's explicit request, and stored; a
// call never reaches this package.
package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/openaicompatible"
	mp3 "github.com/hajimehoshi/go-mp3"

	"github.com/azrtydxb/hello/internal/media"
)

// DefaultBaseURL is OpenRouter's API root.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// MaxTextChars bounds the text of one prompt. It keeps one generation cheap
// and well inside the model limits; a prompt is a sentence or a paragraph.
const MaxTextChars = 2500

// DefaultTimeout bounds one call to OpenRouter.
const DefaultTimeout = 60 * time.Second

// maxAudioBytes bounds what is read back from OpenRouter.
const maxAudioBytes = 32 << 20

// Errors the API turns into clear operator messages.
var (
	ErrNoKey        = errors.New("no OpenRouter API key is configured; set it in Settings, Voice (TTS)")
	ErrUnauthorized = errors.New("OpenRouter rejected the API key (401); check or replace the key in Settings, Voice (TTS)")
	ErrQuota        = errors.New("OpenRouter refused the request: credits exhausted or rate limited; add credits or retry later")
	ErrEmptyAudio   = errors.New("OpenRouter returned no audio")
	ErrBadAudio     = errors.New("OpenRouter returned audio that could not be decoded")
	ErrEmptyText    = errors.New("the text is empty")
	ErrTooLong      = fmt.Errorf("the text is longer than %d characters", MaxTextChars)
)

// Model is one OpenRouter text-to-speech model and the voices it offers.
type Model struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Voices []string `json:"voices"`
}

// Client talks to OpenRouter. The zero value works against DefaultBaseURL.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Timeout time.Duration
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// noRetries makes every SDK call a single attempt: an operator is waiting
// on the answer and retries it themselves.
var noRetries = 0

// provider is go-ai-sdk's OpenAI-compatible provider for OpenRouter with
// key (none is sent when key is empty) and the client's HTTP client.
func (c *Client) provider(key string) *openaicompatible.Provider {
	return openaicompatible.New(c.base(),
		openaicompatible.WithAPIKey(key),
		openaicompatible.WithName("openrouter"),
		openaicompatible.WithHTTPClient(c.http()))
}

// ValidateText checks the text of a prompt.
func ValidateText(text string) error {
	n := len([]rune(strings.TrimSpace(text)))
	if n == 0 {
		return ErrEmptyText
	}
	if n > MaxTextChars {
		return ErrTooLong
	}
	return nil
}

// Synthesize generates speech for text with the given OpenRouter model (for
// example elevenlabs/eleven-multilingual-v2) and voice and returns it as an
// 16-bit mono PCM WAV. It asks for MP3, which carries its own sample rate,
// and keeps it (a wideband voice is not squashed to 8 kHz; playback
// resamples per call). The key is sent to
// OpenRouter and nowhere else; it is never part of a returned error.
func (c *Client) Synthesize(ctx context.Context, key, model, voice, text string) ([]byte, error) {
	if key == "" {
		return nil, ErrNoKey
	}
	if err := ValidateText(text); err != nil {
		return nil, err
	}
	if model == "" {
		return nil, errors.New("no TTS model is selected")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	resp, err := c.provider(key).Speech(model).GenerateSpeech(ctx, provider.SpeechCall{
		Text: text, Voice: voice, OutputFormat: "mp3",
	})
	if err != nil {
		return nil, mapError(err)
	}
	return MP3ToWAV(resp.Audio)
}

// MP3ToWAV decodes MP3 audio to 16-bit mono PCM in a WAV at the MP3's rate.
func MP3ToWAV(audio []byte) ([]byte, error) {
	if len(audio) == 0 {
		return nil, ErrEmptyAudio
	}
	dec, err := mp3.NewDecoder(bytes.NewReader(audio))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadAudio, err)
	}
	raw, err := io.ReadAll(io.LimitReader(dec, maxAudioBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadAudio, err)
	}
	// The decoder always yields 16-bit little-endian stereo frames.
	frames := len(raw) / 4
	if frames == 0 {
		return nil, ErrEmptyAudio
	}
	mono := make([]byte, 2*frames)
	for i := 0; i < frames; i++ {
		l := int(sampleAt(raw[4*i:]))
		r := int(sampleAt(raw[4*i+2:]))
		putSample(mono[2*i:], int16((l+r)/2)) //nolint:gosec // G115: the mean of two int16 fits int16.
	}
	return PCMToWAV(mono, dec.SampleRate())
}

// sampleAt reads one little-endian 16-bit sample.
func sampleAt(b []byte) int16 {
	//nolint:gosec // G115: reinterpreting the two bytes as a signed sample is the point.
	return int16(binary.LittleEndian.Uint16(b))
}

func putSample(b []byte, v int16) {
	//nolint:gosec // G115: reinterpreting the signed sample as two bytes is the point.
	binary.LittleEndian.PutUint16(b, uint16(v))
}

// PCMToWAV validates 16-bit mono PCM (non-empty, even byte count) and wraps
// it in a WAV at its own rate, reduced to media.MaxStoredRate when above it.
func PCMToWAV(pcm []byte, rate int) ([]byte, error) {
	if len(pcm) == 0 {
		return nil, ErrEmptyAudio
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: odd PCM byte count", ErrBadAudio)
	}
	out := pcm
	if rate > media.MaxStoredRate {
		var err error
		if out, err = media.Resample(pcm, rate, media.MaxStoredRate); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadAudio, err)
		}
		rate = media.MaxStoredRate
	}
	if len(out) == 0 {
		return nil, ErrEmptyAudio
	}
	return media.EncodeWAV(out, rate), nil
}

// mapError turns an SDK or transport failure into an operator message. The
// OpenRouter message is kept (it names the problem) but never the key.
func mapError(err error) error {
	var api *ai.APICallError
	if errors.As(err, &api) {
		switch api.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrUnauthorized
		case http.StatusPaymentRequired, http.StatusTooManyRequests:
			return ErrQuota
		}
		msg := api.Message
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("OpenRouter answered %d: %s", api.StatusCode, msg)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("OpenRouter did not answer in time; try again")
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("could not reach OpenRouter: %w", unwrapURL(err))
}

// unwrapURL drops the request URL from a transport error.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// CheckKey asks OpenRouter whether the key is valid, through go-ai-sdk's
// key check (GET /key, which costs nothing).
func (c *Client) CheckKey(ctx context.Context, key string) error {
	if key == "" {
		return ErrNoKey
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	if _, err := ai.CheckKey(ctx, ai.CheckKeyOpts{
		Checker:    c.provider(key).KeyChecker(),
		MaxRetries: &noRetries,
	}); err != nil {
		return mapError(err)
	}
	return nil
}

// Voices lists the ElevenLabs text-to-speech models OpenRouter offers and
// each model's voices, through go-ai-sdk's model listing
// (GET /models?output_modalities=speech, the supported_voices field). The
// key is optional: OpenRouter's model list is public, so without one no
// Authorization header is sent.
func (c *Client) Voices(ctx context.Context, key string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	list, err := ai.ListModels(ctx, ai.ListModelsOpts{
		Lister:         c.provider(key).ModelLister(),
		OutputModality: "speech",
		MaxRetries:     &noRetries,
	})
	if err != nil {
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		if errors.As(err, &syn) || errors.As(err, &typ) {
			return nil, errors.New("OpenRouter sent a model list that could not be read")
		}
		return nil, mapError(err)
	}
	out := []Model{}
	for _, m := range list {
		if !strings.HasPrefix(m.ID, "elevenlabs/") {
			continue
		}
		v := m.Voices
		if v == nil {
			v = []string{}
		}
		out = append(out, Model{ID: m.ID, Name: m.Name, Voices: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
