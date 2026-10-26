package media

import "fmt"

// WAVToRate decodes a WAV file (PCM or float, any channel count, any sample
// rate; Hello stores uploads as 16-bit mono at their own rate up to 48 kHz)
// into 16-bit mono PCM at the given session rate, resampling when the file's
// rate differs: 8 kHz for G.711, 16 kHz for G.722, 48 kHz for Opus. Existing
// 8 kHz prompts therefore play unchanged on a G.711 session and are
// interpolated up on a wideband one, and a 48 kHz asset is filtered down
// once for a narrower session instead of being squashed at upload time.
func WAVToRate(wav []byte, rate int) ([]byte, error) {
	pcm, have, err := DecodeWAV(wav)
	if err != nil {
		return nil, err
	}
	if have == rate {
		return pcm, nil
	}
	pcm = pcm[:len(pcm)&^1]
	out, err := Resample(pcm, have, rate)
	if err != nil {
		return nil, fmt.Errorf("media: resample %d -> %d Hz: %w", have, rate, err)
	}
	return out, nil
}
