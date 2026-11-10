package prov

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// toneWAV builds a WAV with the given format and n samples of silence.
func toneWAV(tag, ch uint16, rate uint32, bits uint16, n int) []byte {
	var b bytes.Buffer
	data := make([]byte, n*int(bits/8)*int(max(ch, 1)))
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, tag)
	_ = binary.Write(&b, binary.LittleEndian, ch)
	_ = binary.Write(&b, binary.LittleEndian, rate)
	_ = binary.Write(&b, binary.LittleEndian, rate*uint32(ch)*uint32(bits/8))
	_ = binary.Write(&b, binary.LittleEndian, ch*bits/8)
	_ = binary.Write(&b, binary.LittleEndian, bits)
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// renderRingCustom is renderRing with a tone library.
func renderRingCustom(t *testing.T, v Vendor, model string, tones map[string]string, customs map[string]bool) string {
	t.Helper()
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.Ring = NewRingInfoCustom(v, model, ringTestTags, tones, customs)
	name := map[Vendor]string{
		Yealink:     d.Phone.MAC + ".cfg",
		Poly:        d.Phone.MAC + "-hello.cfg",
		Grandstream: "cfg" + d.Phone.MAC + ".xml",
		Snom:        d.Phone.MACUpper,
		Fanvil:      d.Phone.MAC + ".cfg",
	}[v]
	b, err := Render(context.Background(), builtinFor(t, v), name, d)
	if err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	return string(b)
}

// TestRingtoneLibrary fails if a WAV outside the S-43 format limits is
// accepted, if a tone is served without the phone's token, over plain HTTP or
// without ETag and 304, if a name outside the library or the allowed form is
// served, if the fetch is not audited as a ringtone, or if a Grandstream,
// Poly or Fanvil template references a custom tone.
func TestRingtoneLibrary(t *testing.T) {
	ok := toneWAV(1, 1, 8000, 16, 8000)
	if info, err := ParseToneWAV(ok); err != nil || info.SampleRate != 8000 || info.Duration != time.Second {
		t.Fatalf("a valid 8 kHz tone: %+v, %v", info, err)
	}
	if info, err := ParseToneWAV(toneWAV(1, 1, 16000, 16, 16000*8)); err != nil || info.SampleRate != 16000 || info.Duration != 8*time.Second {
		t.Fatalf("a valid 16 kHz tone: %+v, %v", info, err)
	}
	refused := map[string][]byte{
		"empty":       nil,
		"not a wav":   []byte("this is not a wave file at all"),
		"stereo":      toneWAV(1, 2, 8000, 16, 800),
		"44.1 kHz":    toneWAV(1, 1, 44100, 16, 800),
		"8-bit":       toneWAV(1, 1, 8000, 8, 800),
		"24-bit":      toneWAV(1, 1, 8000, 24, 800),
		"mu-law":      toneWAV(7, 1, 8000, 8, 800),
		"float":       toneWAV(3, 1, 16000, 32, 800),
		"no audio":    toneWAV(1, 1, 8000, 16, 0),
		"over 512KiB": toneWAV(1, 1, 16000, 16, 16000*17),
	}
	for name, data := range refused {
		if _, err := ParseToneWAV(data); !errors.Is(err, ErrTone) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// 30 s at 8 kHz is 480 000 bytes, inside 512 KiB; 31 s is 496 000, also
	// inside, so the duration limit is what refuses it.
	if _, err := ParseToneWAV(toneWAV(1, 1, 8000, 16, 8000*30)); err != nil {
		t.Errorf("30 s refused: %v", err)
	}
	if _, err := ParseToneWAV(toneWAV(1, 1, 8000, 16, 8000*31)); !errors.Is(err, ErrTone) {
		t.Errorf("31 s accepted: %v", err)
	}
	if _, err := ParseToneWAV(ok[:len(ok)-5000]); err != nil {
		t.Errorf("a data chunk longer than the file is the bytes present: %v", err)
	}
	for _, n := range []string{"a", "doorbell", "two-tone-2", strings.Repeat("a", 32)} {
		if !ValidToneName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "-a", "A", "a b", "a_b", "a.wav", "../x", strings.Repeat("a", 33)} {
		if ValidToneName(n) {
			t.Errorf("%q accepted", n)
		}
	}

	e := newEnv(t)
	_, tok := e.s.addPhone(Yealink, "T54W", macA)
	e.s.addPhone(Yealink, "T54W", macB)
	tone := Ringtone{ID: 1, Name: "chime", ObjectKey: "chime.wav", SHA256: strings.Repeat("c", 64), UploadedAt: time.Now()}
	e.s.tones["chime"] = tone
	e.o[tone.ObjectKey] = ok

	w := e.do(req{path: devPath(tok, "ring/chime.wav")})
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), ok) || w.Header().Get("ETag") != `"`+tone.SHA256+`"` ||
		w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("tone: %d %q %q", w.Code, w.Header().Get("ETag"), w.Header().Get("Content-Type"))
	}
	if row := e.last(); row.Kind != KindRingtone || row.Result != ResultServed || row.Bytes != int64(len(ok)) || strings.Contains(row.PathRedacted, tok) {
		t.Fatalf("audit row %+v", row)
	}
	if row := expectEmpty(t, e, e.do(req{path: devPath(tok, "ring/chime.wav"), inm: `"` + tone.SHA256 + `"`}), 304, ResultNotModified); row.Kind != KindRingtone {
		t.Fatalf("304 row %+v", row)
	}
	if w := e.do(req{path: devPath(tok, "ring/chime.wav"), inm: `"other"`}); w.Code != http.StatusOK {
		t.Fatalf("a stale ETag: %d", w.Code)
	}
	expectEmpty(t, e, e.do(req{path: devPath("0123456789abcdef0123456789abcdef0123456789a", "ring/chime.wav")}), 404, ResultUnknownToken)
	expectEmpty(t, e, e.do(req{path: "/p/boot/ring/chime.wav"}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "ring/chime.wav"), proto: "http"}), 404, ResultPlainHTTP)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "ring/missing.wav")}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "ring/chime.mp3")}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{method: http.MethodPut, path: devPath(tok, "ring/chime.wav"), body: "x"}), 404, ResultNotFound)
	delete(e.o, tone.ObjectKey)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "ring/chime.wav")}), 503, ResultUnavailable)

	// A custom tone is referenced by Yealink and Snom only, and only while it is in the library.
	custom := map[string]bool{"chime": true}
	tones := map[string]string{"doorbell": CustomPrefix + "chime", "alarm": CustomPrefix + "ghost"}
	y := renderRingCustom(t, Yealink, "T54W", tones, custom)
	wantLines(t, "yealink", y, "ringtone.url = https://", "/ring/chime.wav\n", "distinctive_ring_tones.alert_info.5.ringer = chime.wav\n")
	wantNoLines(t, "yealink", y, "ghost")
	s := renderRingCustom(t, Snom, "D785", map[string]string{"external": CustomPrefix + "chime"}, custom)
	wantLines(t, "snom", s, "ring/chime.wav</custom_melody_url>", `<alert_external_ring_sound perm="R">Custom</alert_external_ring_sound>`)
	for _, v := range []Vendor{Poly, Grandstream, Fanvil} {
		model := map[Vendor]string{Poly: "VVX 450", Grandstream: "WP822", Fanvil: "X6U"}[v]
		out := renderRingCustom(t, v, model, tones, custom)
		wantNoLines(t, string(v), out, "chime", "/ring/", "custom:")
	}
	// Only one custom file is delivered: a second custom tone plays the default.
	two := renderRingCustom(t, Yealink, "T54W", map[string]string{"doorbell": CustomPrefix + "chime", "alarm": CustomPrefix + "bell"}, map[string]bool{"chime": true, "bell": true})
	wantLines(t, "yealink two", two, "alert_info.5.ringer = chime.wav\n")
	wantNoLines(t, "yealink two", two, "bell.wav")
	// A tone left out of the library is not referenced.
	none := renderRingCustom(t, Yealink, "T54W", tones, nil)
	wantNoLines(t, "yealink none", none, "ringtone.url", "chime")
}
