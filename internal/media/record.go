// Call recording (plan contract 5): the pass-through tap of an anchored
// session's RTP payloads into a WAV (G.711, G.722 and Opus payloads are
// decoded and the directions mixed; any other codec's payload is stored
// pass-through with the codec noted), the MinIO object keys of contract 4, and the in-memory
// bound of the spec's failure mode (a recording holds at most 10 MB).
package media

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxRecordingBytes bounds a recording's in-memory hold: at most 10 MB
// (spec failure mode "MinIO unavailable at recording end").
const maxRecordingBytes = 10 << 20

// RecordingKey builds contract 4's recording key rec/<unix>-<callid>.wav.
// The Call-ID is attacker-influenced and validated like ObjectKey's: no
// separators, no dot-dot, never empty (refuse, never sanitise).
func RecordingKey(callID string, at time.Time) (string, error) {
	if callID == "" {
		return "", errors.New("media: call ID is empty")
	}
	if strings.ContainsAny(callID, "/\\") {
		return "", errors.New("media: call ID contains a path separator")
	}
	if strings.Contains(callID, "..") {
		return "", errors.New("media: call ID contains dot-dot")
	}
	return fmt.Sprintf("rec/%d-%s.wav", at.Unix(), callID), nil
}

// AnnouncementKey builds contract 4's announcement key ann/<name>.wav. The
// name is validated against the announcements table's shape (migration
// 00005) even though the store validates it too: refuse, never sanitise.
func AnnouncementKey(name string) (string, error) {
	if !announcementRe.MatchString(name) {
		return "", fmt.Errorf("media: announcement name %q does not fit ^[A-Za-z0-9._-]{1,64}$", name)
	}
	return "ann/" + name + ".wav", nil
}

// announcementRe is the announcements.name shape of migration 00005.
var announcementRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Recorder captures one recorded call's audio. The B2BUA feeds it the
// pass-through payloads of the anchored session's audio packets (both
// directions) with Write, pauses it while the call is held, and takes the
// WAV with Result when the recording stops. G.711, G.722 and Opus payloads
// are decoded (the relay passes payloads through untouched, so the tap is
// where wideband audio becomes PCM); each direction is kept at its own
// codec's sample rate and the mix is made at the highest. Safe for
// concurrent use.
type Recorder struct {
	mu sync.Mutex
	// pcm holds the decoded audio per leg, in arrival order, at rate[leg].
	pcm  map[string][]byte
	rate map[string]int
	// codecs maps negotiated payload types to codecs; the static G.711 and
	// G.722 types are always known.
	codecs   map[uint8]Codec
	decoders map[string]*Decoder // per leg and payload type
	raw      []byte              // pass-through payload when a direction is not decodable
	codec    string
	maxBytes int
	over     bool // the 10 MB bound was hit; the recording is truncated
}

// NewRecorder returns a recorder with the 10 MB bound.
func NewRecorder() *Recorder {
	return &Recorder{
		pcm: map[string][]byte{}, rate: map[string]int{},
		codecs: map[uint8]Codec{}, decoders: map[string]*Decoder{},
		maxBytes: maxRecordingBytes,
	}
}

// SetCodec tells the recorder which codec a payload type carries; it is how
// a dynamic payload type (Opus, usually 96-127) becomes decodable.
func (rec *Recorder) SetCodec(c Codec) {
	rec.mu.Lock()
	rec.codecs[c.PT] = c
	rec.mu.Unlock()
}

// codecFor resolves a payload type under the lock.
func (rec *Recorder) codecFor(pt uint8) (Codec, bool) {
	if c, ok := rec.codecs[pt]; ok {
		return c, c.Supported()
	}
	c, ok := StaticCodec(pt)
	return c, ok
}

// Write captures one audio payload from one leg. Payloads of a known codec
// (G.711, G.722, Opus) are decoded to PCM; any other payload type is kept
// pass-through with the codec noted (spec edge case "codec mismatch after
// re-INVITE").
func (rec *Recorder) Write(leg string, pt uint8, payload []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	c, ok := rec.codecFor(pt)
	if ok && rec.raw == nil {
		key := fmt.Sprintf("%s/%d", leg, pt)
		dec := rec.decoders[key]
		if dec == nil {
			d, err := NewDecoder(c)
			if err != nil {
				ok = false
			} else {
				dec = d
				rec.decoders[key] = d
			}
		}
		if ok {
			decoded := dec.Decode(payload)
			if rate := c.SampleRate(); rec.rate[leg] != 0 && rec.rate[leg] != rate {
				// The leg changed codec mid-call: bring the new audio to
				// the rate the leg's recording already has.
				if r, err := Resample(decoded, rate, rec.rate[leg]); err == nil {
					decoded = r
				}
			} else {
				rec.rate[leg] = rate
			}
			if rec.room(len(decoded)) {
				rec.pcm[leg] = append(rec.pcm[leg], decoded...)
			}
			return
		}
	}
	if ok {
		return // the recording is raw since an earlier packet; stay raw
	}
	if rec.raw == nil {
		rec.codec = CodecName(pt)
		rec.raw = []byte{}
	}
	if rec.room(len(payload)) {
		rec.raw = append(rec.raw, payload...)
	}
}

// room reports whether n more bytes still fit under the bound; a payload
// that does not fit sets over and is dropped.
func (rec *Recorder) room(n int) bool {
	used := len(rec.raw)
	for _, p := range rec.pcm {
		used += len(p)
	}
	if used+n <= rec.maxBytes {
		return true
	}
	rec.over = true
	return false
}

// Result takes the recording: the WAV of the mixed directions, or the
// pass-through payload with the codec noted when any direction was not
// decodable. durMs is the longest direction's duration in ms. The WAV's
// sample rate is the highest of the directions' (16 kHz when either side
// spoke G.722 or Opus, else 8 kHz); slower directions are resampled up.
func (rec *Recorder) Result() (audio []byte, codec string, durMs int64) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for k, d := range rec.decoders {
		d.Close()
		delete(rec.decoders, k)
	}
	if rec.raw != nil {
		return rec.raw, rec.codec, 0
	}
	mixRate := sampleRateHz
	for leg, p := range rec.pcm {
		if len(p) > 0 && rec.rate[leg] > mixRate {
			mixRate = rec.rate[leg]
		}
	}
	dirs := map[string][]byte{}
	var maxLen int
	for leg, p := range rec.pcm {
		if len(p) == 0 {
			continue
		}
		if r := rec.rate[leg]; r != 0 && r != mixRate {
			if up, err := Resample(p, r, mixRate); err == nil {
				p = up
			}
		}
		dirs[leg] = p
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	if maxLen == 0 {
		return nil, "", 0
	}
	mixed := MixPCM(dirs)
	return EncodeWAV(mixed, mixRate), "WAV", int64(maxLen/2) * 1000 / int64(mixRate)
}

// Over reports whether the recording hit the 10 MB bound and was
// truncated.
func (rec *Recorder) Over() bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.over
}

// CodecName names a payload type for the recording metadata; a dynamic
// payload type is named pt/<n> since Hello never transcodes.
func CodecName(pt uint8) string {
	switch pt {
	case 0:
		return "PCMU"
	case 8:
		return "PCMA"
	case 9:
		return "G722"
	}
	return fmt.Sprintf("pt/%d", pt)
}

// MixPCM mixes per-direction PCM buffers into one mono track by saturating
// addition, padding shorter directions with silence.
func MixPCM(dirs map[string][]byte) []byte {
	var maxLen int
	for _, p := range dirs {
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	out := make([]byte, maxLen)
	for _, p := range dirs {
		for i := 0; i+1 < len(p); i += 2 {
			a := int16(uint16(p[i]) | uint16(p[i+1])<<8)     //nolint:gosec // 16-bit sample
			b := int16(uint16(out[i]) | uint16(out[i+1])<<8) //nolint:gosec // 16-bit sample
			sum := int32(a) + int32(b)
			if sum > 32767 {
				sum = 32767
			}
			if sum < -32768 {
				sum = -32768
			}
			out[i] = byte(sum)        //nolint:gosec // low byte of a 16-bit sample
			out[i+1] = byte(sum >> 8) //nolint:gosec // high byte
		}
	}
	return out
}
