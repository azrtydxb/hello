package media

import (
	"strings"
	"testing"
	"time"
)

// TestRecordingKey fails if a hostile Call-ID is sanitised instead of
// refused, or if the key shape of contract 4 (rec/<unix>-<callid>.wav)
// drifts. Mutation: dropping the separator check stores audio under
// attacker-chosen keys.
func TestRecordingKey(t *testing.T) {
	k, err := RecordingKey("abc@host", time.Unix(1700000000, 0))
	if err != nil || k != "rec/1700000000-abc@host.wav" {
		t.Fatalf("key = %q, %v", k, err)
	}
	for _, bad := range []string{"", "a/b", "a\\b", "..", "x..y"} {
		if _, err := RecordingKey(bad, time.Now()); err == nil {
			t.Errorf("call ID %q accepted", bad)
		}
	}
}

// TestAnnouncementKey fails if a name outside the migration's CHECK shape
// is accepted.
func TestAnnouncementKey(t *testing.T) {
	k, err := AnnouncementKey("welcome-en")
	if err != nil || k != "ann/welcome-en.wav" {
		t.Fatalf("key = %q, %v", k, err)
	}
	for _, bad := range []string{"", "bad name", strings.Repeat("x", 65), "a/b"} {
		if _, err := AnnouncementKey(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

// TestRecorderMix fails if the mixed WAV is empty, not a WAV, or if the
// duration does not track the longest direction.
func TestRecorderMix(t *testing.T) {
	rec := NewRecorder()
	rec.Write("a", 0, EncodePCMU(make([]byte, 320))) // 20 ms of silence
	rec.Write("b", 8, EncodePCMA(make([]byte, 640))) // 40 ms of silence
	audio, codec, durMs := rec.Result()
	if codec != "WAV" || durMs != 40 {
		t.Fatalf("codec=%q durMs=%d, want WAV/40", codec, durMs)
	}
	if len(audio) < 44 || string(audio[:4]) != "RIFF" {
		t.Fatalf("not a WAV: %d bytes", len(audio))
	}
}

// TestRecorderPassthrough fails if a non-G.711 payload is not stored
// pass-through with the codec noted (spec edge case).
func TestRecorderPassthrough(t *testing.T) {
	rec := NewRecorder()
	rec.Write("a", 96, []byte("opus-frame"))
	audio, codec, _ := rec.Result()
	if codec != "pt/96" || string(audio) != "opus-frame" {
		t.Fatalf("codec=%q audio=%q, want pt/96 pass-through", codec, audio)
	}
}
