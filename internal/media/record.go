// Call recording (plan contract 5): the pass-through tap of an anchored
// session's RTP payloads into a WAV (G.711 payloads are decoded and the
// directions mixed; any other codec's payload is stored pass-through with
// the codec noted), the MinIO object keys of contract 4, and the in-memory
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
// WAV with Result when the recording stops. Safe for concurrent use.
type Recorder struct {
	mu sync.Mutex
	// pcm holds the decoded audio per leg, in arrival order.
	pcm      map[string][]byte
	raw      []byte // pass-through payload when a direction is not G.711
	codec    string
	maxBytes int
	over     bool // the 10 MB bound was hit; the recording is truncated
}

// NewRecorder returns a recorder with the 10 MB bound.
func NewRecorder() *Recorder {
	return &Recorder{pcm: map[string][]byte{}, maxBytes: maxRecordingBytes}
}

// Write captures one audio payload from one leg. G.711 payloads (0 PCMU,
// 8 PCMA) are decoded to PCM; any other payload type is kept pass-through
// with the codec noted (spec edge case "codec mismatch after re-INVITE").
func (rec *Recorder) Write(leg string, pt uint8, payload []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	switch pt {
	case 0, 8:
		if rec.raw != nil {
			return // the recording is raw since an earlier packet; stay raw
		}
		decoded := DecodePCMU(payload)
		if pt == 8 {
			decoded = DecodePCMA(payload)
		}
		if rec.room(len(decoded)) {
			rec.pcm[leg] = append(rec.pcm[leg], decoded...)
		}
	default:
		if rec.raw == nil {
			rec.codec = CodecName(pt)
			rec.raw = []byte{}
		}
		if rec.room(len(payload)) {
			rec.raw = append(rec.raw, payload...)
		}
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

// Result takes the recording: the WAV of the mixed G.711 directions, or
// the pass-through payload with the codec noted when any direction was
// not G.711. durMs is the longest direction's duration in ms.
func (rec *Recorder) Result() (audio []byte, codec string, durMs int64) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.raw != nil {
		return rec.raw, rec.codec, 0
	}
	var maxLen int
	for _, p := range rec.pcm {
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	if maxLen == 0 {
		return nil, "", 0
	}
	mixed := MixPCM(rec.pcm)
	return EncodeWAV(mixed, sampleRateHz), "WAV", int64(maxLen/2) * 1000 / sampleRateHz
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
