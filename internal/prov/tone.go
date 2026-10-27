package prov

import (
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// The tone library (phone-feature-parity S-43): WAV files the admin uploads,
// kept in MinIO (bucket hello-ringtones) and served to a phone, under its
// token, at /p/<token>/ring/<name>.wav.

// Tone limits (S-43): PCM, 8 or 16 kHz, mono, 16-bit, at most 30 s and 512 KiB.
const (
	MaxToneBytes    = 512 << 10
	MaxToneDuration = 30 * time.Second
)

var toneNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidToneName reports whether name is a tone's name: 1 to 32 of a-z, 0-9
// and "-", starting with a letter or digit.
func ValidToneName(name string) bool { return toneNameRE.MatchString(name) }

// Ringtone is one tone of the library (a ringtones row).
type Ringtone struct {
	ID         int64
	Name       string
	ObjectKey  string // in bucket hello-ringtones
	Size       int64
	SHA256     string // lowercase hex; the tone's ETag
	SampleRate int
	DurationMS int
	UploadedAt time.Time
}

// ToneInfo is what the format check learns of a WAV.
type ToneInfo struct {
	SampleRate int
	Duration   time.Duration
}

// ErrTone is wrapped by every refusal of ParseToneWAV; its message is fit
// for the admin.
var ErrTone = errors.New("tone")

func toneErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrTone, fmt.Sprintf(format, a...))
}

// ParseToneWAV checks data against the S-43 limits and returns the tone's
// sample rate and duration. The bytes decide; nothing is converted.
func ParseToneWAV(data []byte) (ToneInfo, error) {
	if len(data) == 0 {
		return ToneInfo{}, toneErr("a WAV file is required")
	}
	if len(data) > MaxToneBytes {
		return ToneInfo{}, toneErr("the file is larger than 512 KiB")
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return ToneInfo{}, toneErr("the file must be a RIFF/WAVE file")
	}
	var (
		haveFmt  bool
		rate     int
		dataSize = -1
	)
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if id == "data" {
			// A streamed WAV may claim more than is there; the bytes present count.
			dataSize = min(size, len(data)-body)
			break
		}
		if size < 0 || body+size > len(data) {
			return ToneInfo{}, toneErr("the WAV file is truncated")
		}
		if id == "fmt " {
			if size < 16 {
				return ToneInfo{}, toneErr("the WAV format chunk is too short")
			}
			f := data[body : body+size]
			tag := binary.LittleEndian.Uint16(f[0:2])
			ch := binary.LittleEndian.Uint16(f[2:4])
			rate = int(binary.LittleEndian.Uint32(f[4:8]))
			bits := binary.LittleEndian.Uint16(f[14:16])
			switch {
			case tag != 1:
				return ToneInfo{}, toneErr("the audio must be uncompressed PCM")
			case ch != 1:
				return ToneInfo{}, toneErr("the audio must be mono")
			case rate != 8000 && rate != 16000:
				return ToneInfo{}, toneErr("the sample rate must be 8000 or 16000 Hz")
			case bits != 16:
				return ToneInfo{}, toneErr("the audio must be 16-bit")
			}
			haveFmt = true
		}
		off = body + size + size&1 // chunks are word aligned
	}
	if !haveFmt {
		return ToneInfo{}, toneErr("the WAV file has no format chunk")
	}
	if dataSize <= 0 {
		return ToneInfo{}, toneErr("the WAV file has no audio")
	}
	d := time.Duration(dataSize/2) * time.Second / time.Duration(rate)
	if d > MaxToneDuration {
		return ToneInfo{}, toneErr("the tone is longer than 30 seconds")
	}
	return ToneInfo{SampleRate: rate, Duration: d}, nil
}
