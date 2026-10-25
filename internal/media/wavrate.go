package media

import "fmt"

// WAVToRate decodes a WAV file (16-bit mono PCM at any sample rate; 8 kHz
// and 16 kHz are what Hello stores) into PCM at the given session rate,
// resampling when the file's rate differs. Existing 8 kHz prompts therefore
// play unchanged on a G.711 session and are interpolated up on a G.722
// session, and 16 kHz prompts and recordings are reduced for G.711.
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
