package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func corpus(t *testing.T) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob("testdata/sdp/*.sdp")
	if err != nil || len(files) < 7 {
		t.Fatalf("corpus: %v, %d files", err, len(files))
	}
	out := map[string][]byte{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = b
	}
	return out
}

func lines(b []byte) []string {
	return strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
}

// sectionLines returns the lines of the n-th m= section (0 based).
func sectionLines(b []byte, n int) []string {
	var out []string
	idx := -1
	for _, l := range lines(b) {
		if strings.HasPrefix(l, "m=") {
			idx++
		}
		if idx == n {
			out = append(out, l)
		}
	}
	return out
}

func TestSDPModelRoundTrip(t *testing.T) {
	for name, body := range corpus(t) {
		t.Run(name, func(t *testing.T) {
			s, err := ParseSDPSession(body)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got, err := s.Build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			want, have := lines(body), lines(got)
			if len(want) != len(have) {
				t.Fatalf("line count %d, want %d\n%s", len(have), len(want), got)
			}
			for i := range want {
				if want[i] != have[i] {
					t.Errorf("line %d: %q, want %q", i, have[i], want[i])
				}
			}
			// The audio sections are a no-op on their own, and a bare LF body
			// parses to the same session.
			for i, m := range s.Media() {
				if m.Kind() != "audio" {
					continue
				}
				a, b := strings.Join(sectionLines(body, i), "\n"), strings.Join(sectionLines(got, i), "\n")
				if a != b {
					t.Errorf("audio section %d changed:\n%s\nwant\n%s", i, b, a)
				}
			}
			lf, err := ParseSDPSession([]byte(strings.ReplaceAll(string(body), "\r\n", "\n")))
			if err != nil {
				t.Fatalf("parse LF: %v", err)
			}
			if again, _ := lf.Build(); string(again) != string(got) {
				t.Errorf("LF body built differently")
			}
		})
	}
}

func TestSDPModelBrowserOffer(t *testing.T) {
	s, err := ParseSDPSession(corpus(t)["chrome_offer.sdp"])
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Bundle(); len(got) != 1 || got[0] != "0" {
		t.Errorf("bundle %v", got)
	}
	m := s.Media()[0]
	if m.Kind() != "audio" || m.Proto() != ProtoDTLSFB || m.Mid() != "0" || !m.RTCPMux() || m.Direction() != "sendrecv" {
		t.Errorf("section: %s %s mid %q mux %v dir %s", m.Kind(), m.Proto(), m.Mid(), m.RTCPMux(), m.Direction())
	}
	ice := m.ICE(s)
	if ice.Ufrag != "rK7q" || len(ice.Pwd) != 24 || ice.Lite || len(ice.Candidates) != 2 || ice.Options != "trickle" {
		t.Errorf("ice %+v", ice)
	}
	d := m.DTLS(s)
	if d.Setup != "actpass" || len(d.Fingerprints) != 1 || !fingerprintUsable(d.Fingerprints[0]) {
		t.Errorf("dtls %+v", d)
	}
	if ex := m.Extmaps(); len(ex) != 4 || ex[0].ID != 1 || !strings.Contains(ex[0].URI, "ssrc-audio-level") {
		t.Errorf("extmaps %+v", ex)
	}
	cs := m.Codecs()
	if len(cs) != 8 || cs[0].Name != "opus" || cs[0].Rate != 48000 || cs[0].Channels != 2 ||
		cs[0].Fmtp != "minptime=10;useinbandfec=1" || len(cs[0].Feedback) != 1 || cs[0].Feedback[0] != "transport-cc" {
		t.Errorf("codecs %+v", cs)
	}
	if cs[2].Name != "G722" || cs[3].Name != "PCMU" {
		t.Errorf("static codecs %+v %+v", cs[2], cs[3])
	}
	if err := ValidateWebRTC(s); err != nil {
		t.Errorf("a Chrome offer must validate: %v", err)
	}
}

func TestSDPModelSessionLevelAttributes(t *testing.T) {
	// Firefox puts the fingerprint at the session level.
	s, err := ParseSDPSession(corpus(t)["firefox_offer.sdp"])
	if err != nil {
		t.Fatal(err)
	}
	m := s.Media()[0]
	if d := m.DTLS(s); len(d.Fingerprints) != 1 || d.Setup != "actpass" {
		t.Errorf("dtls %+v", d)
	}
	if ex := m.Extmaps(); len(ex) != 3 || ex[1].Direction != "recvonly" {
		t.Errorf("extmaps %+v", ex)
	}
	if err := ValidateWebRTC(s); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestSDPModelVideoSection(t *testing.T) {
	s, err := ParseSDPSession(corpus(t)["safari_offer.sdp"])
	if err != nil {
		t.Fatal(err)
	}
	ms := s.Media()
	if len(ms) != 2 || ms[1].Kind() != "video" || ms[1].Mid() != "1" || !ms[1].RTCPRsize() {
		t.Fatalf("sections %d", len(ms))
	}
	var h264 SDPCodec
	for _, c := range ms[1].Codecs() {
		if c.Name == "H264" {
			h264 = c
		}
	}
	if h264.PT != 96 || h264.Rate != 90000 || len(h264.Feedback) != 5 || !strings.Contains(h264.Fmtp, "packetization-mode=1") {
		t.Errorf("h264 %+v", h264)
	}
	if got := s.Bundle(); len(got) != 2 {
		t.Errorf("bundle %v", got)
	}
}

func TestSDPModelSDES(t *testing.T) {
	s, err := ParseSDPSession(corpus(t)["sdes.sdp"])
	if err != nil {
		t.Fatal(err)
	}
	m := s.Media()[0]
	keys, skipped := m.Crypto()
	if len(keys) != 2 || skipped != 0 {
		t.Errorf("crypto %d skipped %d", len(keys), skipped)
	}
	if p, ok := ProfileOf(m.Proto()); !ok || p != ProfileSDES {
		t.Errorf("profile %v", p)
	}
	if s.SessionHasCleartext() {
		t.Error("an SAVP section with keys is not cleartext")
	}
}

func TestSDPModelRejectsGarbage(t *testing.T) {
	for _, b := range []string{"", "hello", "v=0\r\nm=audio x RTP/AVP 0\r\n"} {
		if _, err := ParseSDPSession([]byte(b)); err == nil {
			t.Errorf("%q parsed", b)
		}
	}
}

// Every vendor SDP of the corpus that carries audio bridges to the AudioSDP
// the call code uses, with the same codec the audio-only parser picks.
func TestAudioSectionBridge(t *testing.T) {
	for name, body := range corpus(t) {
		if strings.Contains(name, "sdes") {
			continue
		}
		s, err := ParseSDPSession(body)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Media()[0].AudioSection(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Codec.Name == "" || got.Port != s.Media()[0].Port() {
			t.Errorf("%s: bridged codec %+v port %d", name, got.Codec, got.Port)
		}
		if strings.Contains(name, "offer") {
			continue // a browser offer has no usable address in c=
		}
		direct, err := ParseAudioSDP(body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if direct.Codec != got.Codec || direct.Address != got.Address || direct.DTMFPayloadType != got.DTMFPayloadType {
			t.Errorf("%s: bridge %+v, direct %+v", name, got.Codec, direct.Codec)
		}
	}
}

// ParseAudioSDP is not built on the model: phones and trunks send SDPs the
// strict reader refuses (no o= or s= line, bare LF, a missing t=), and call
// setup must still answer them. This pins that difference so a later change
// that unifies the two notices it.
func TestParseAudioSDPStaysTolerant(t *testing.T) {
	for name, body := range map[string]string{
		"no o= or s=": "v=0\nc=IN IP4 10.0.0.9\nt=0 0\nm=audio 5004 RTP/AVP 0\n",
		"no v= line":  "c=IN IP4 10.0.0.9\nm=audio 5004 RTP/AVP 0 8\n",
		"no t= line":  "v=0\no=- 1 1 IN IP4 10.0.0.9\ns=-\nc=IN IP4 10.0.0.9\nm=audio 5004 RTP/AVP 0\n",
	} {
		if _, err := ParseAudioSDP([]byte(body)); err != nil {
			t.Errorf("%s: the audio parser refused it: %v", name, err)
		}
		if _, err := ParseSDPSession([]byte(body)); err == nil {
			t.Errorf("%s: the strict model accepted it, so the two readers could be unified", name)
		}
	}
}
