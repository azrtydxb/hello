package main

import (
	"context"
	"log/slog"

	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/tts"
	"github.com/azrtydxb/hello/internal/ttscache"
)

// newSpeech builds the voicemail voice: prompts are spoken with the model,
// voice and key saved under Settings, Voice (TTS), synthesized through
// OpenRouter once and cached in the voicemail bucket (tts/hd/<hash>.wav) for
// every node. The store must hold the secret box that opens the key. The key
// is read per refresh and never logged.
func newSpeech(st *store.Store, objs sip.ObjectStore, log *slog.Logger) *ttscache.Cache {
	return &ttscache.Cache{
		Store: objs,
		Synth: &tts.Client{},
		Log:   log,
		Settings: func(ctx context.Context) (ttscache.Settings, error) {
			set, err := st.GetTTSSettings(ctx)
			if err != nil {
				return ttscache.Settings{}, err
			}
			if !set.Configured {
				return ttscache.Settings{Model: set.Model, Voice: set.Voice}, nil
			}
			key, err := st.TTSKey(ctx)
			if err != nil {
				return ttscache.Settings{}, err
			}
			return ttscache.Settings{APIKey: key, Model: set.Model, Voice: set.Voice}, nil
		},
	}
}
