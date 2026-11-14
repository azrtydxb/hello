package media

import (
	"strings"
	"testing"
)

const testFP = "sha-256 7B:8B:F0:65:5F:78:E2:51:3B:AC:6F:F3:3F:46:1B:35:DC:B8:5F:64:1A:24:C2:43:F0:A1:58:D0:A1:2C:19:08"

func testLocal() WebRTCLocal {
	return WebRTCLocal{
		Ufrag: "hLo1", Pwd: "0123456789abcdefghijklmn", Hosts: []string{"192.168.10.101", "203.0.113.9"},
		Port: 21100, Fingerprint: testFP, Setup: "passive",
	}
}

func browserOffer(t *testing.T, name string) (*SDPSession, AudioSDP) {
	t.Helper()
	s, err := ParseSDPSession(corpus(t)[name])
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Media()[0].AudioSection(s)
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}

func TestWebRTCCodecSelection(t *testing.T) {
	cases := []struct {
		name    string
		phone   []string
		prefs   []string
		want    string
		wantPT  int
		wantDTM int
	}{
		{"G.711-only phone", []string{"PCMU", "PCMA"}, []string{"g711"}, "PCMU", 0, 126},
		{"G.722 phone", []string{"G722", "PCMU", "PCMA"}, []string{"g722", "g711"}, "G722", 9, 126},
		{"Opus phone", []string{"opus", "G722", "PCMU"}, []string{"opus", "g722", "g711"}, "opus", 111, 110},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			offer, browser := browserOffer(t, "chrome_offer.sdp")
			// What the callee chooses out of what the browser offered it.
			toPhone := browser.Filter(c.phone)
			chosen, ok := toPhone.Pick(c.prefs...)
			if !ok {
				t.Fatal("phone found no codec")
			}
			ans, err := AnswerWebRTC(offer, AnswerFor(browser, chosen), testLocal())
			if err != nil {
				t.Fatal(err)
			}
			m := ans.Media()[0]
			cs := m.Codecs()
			if len(cs) != 2 {
				t.Fatalf("answer lists %+v, want the callee's codec and one telephone-event", cs)
			}
			if cs[0].Name != c.want || int(cs[0].PT) != c.wantPT {
				t.Errorf("answer codec %s/%d, want %s/%d", cs[0].Name, cs[0].PT, c.want, c.wantPT)
			}
			if cs[1].Name != "telephone-event" || int(cs[1].PT) != c.wantDTM {
				t.Errorf("answer event %+v, want pt %d", cs[1], c.wantDTM)
			}
			// Nothing the browser did not offer, nothing the callee did not choose.
			for _, a := range cs {
				found := false
				for _, o := range offer.Media()[0].Codecs() {
					if o.PT == a.PT && strings.EqualFold(o.Name, a.Name) {
						found = true
					}
				}
				if !found {
					t.Errorf("answer lists %+v, which the browser never offered", a)
				}
			}
		})
	}
}

func TestAnswerWebRTCShape(t *testing.T) {
	for _, name := range []string{"chrome_offer.sdp", "firefox_offer.sdp", "safari_offer.sdp"} {
		t.Run(name, func(t *testing.T) {
			offer, browser := browserOffer(t, name)
			chosen, _ := browser.Pick("g722", "g711")
			ans, err := AnswerWebRTC(offer, AnswerFor(browser, chosen), testLocal())
			if err != nil {
				t.Fatal(err)
			}
			body, err := ans.Build()
			if err != nil {
				t.Fatal(err)
			}
			re, err := ParseSDPSession(body)
			if err != nil {
				t.Fatalf("answer does not parse: %v\n%s", err, body)
			}
			if !re.IceLite() {
				t.Error("answer is not ice-lite")
			}
			if err := ValidateWebRTC(re); err != nil {
				t.Errorf("answer fails the WebRTC rules: %v\n%s", err, body)
			}
			if re.SessionHasCleartext() {
				t.Error("answer has a cleartext section")
			}
			if len(re.Media()) != len(offer.Media()) {
				t.Errorf("answer has %d sections for %d offered", len(re.Media()), len(offer.Media()))
			}
			for i, m := range re.Media() {
				if m.Mid() != offer.Media()[i].Mid() {
					t.Errorf("section %d mid %q, offer %q", i, m.Mid(), offer.Media()[i].Mid())
				}
				if m.Kind() == "video" && m.Port() != 0 {
					t.Error("video accepted before the video phases")
				}
			}
			am := re.Media()[0]
			if am.Port() != 21100 || am.Direction() != "sendrecv" || am.DTLS(re).Setup != "passive" {
				t.Errorf("audio port %d dir %s setup %s", am.Port(), am.Direction(), am.DTLS(re).Setup)
			}
			if got := re.Bundle(); len(got) != 1 || got[0] != offer.Media()[0].Mid() {
				t.Errorf("bundle %v: only the accepted section belongs in it", got)
			}
		})
	}
}

func TestICELiteCandidates(t *testing.T) {
	offer, browser := browserOffer(t, "chrome_offer.sdp")
	chosen, _ := browser.Pick("g711")
	ans, err := AnswerWebRTC(offer, AnswerFor(browser, chosen), testLocal())
	if err != nil {
		t.Fatal(err)
	}
	got := ans.CandidateAddresses()
	if len(got) != 2 || got[0] != "192.168.10.101" || got[1] != "203.0.113.9" {
		t.Fatalf("candidates %v, want only the configured host addresses", got)
	}
	m := ans.Media()[0]
	for _, c := range m.Attrs("candidate") {
		if !strings.Contains(c, " typ host") || !strings.Contains(c, " 21100 ") || !strings.Contains(c, " udp ") {
			t.Errorf("candidate %q is not a udp host candidate on the shared port", c)
		}
	}
	if !m.Has("end-of-candidates") {
		t.Error("no end-of-candidates")
	}
	// The browser's own srflx candidate never leaks into the answer.
	for _, a := range got {
		if a == "203.0.113.7" || a == "192.168.1.20" {
			t.Errorf("answer repeats the peer's candidate %s", a)
		}
	}
}

func TestAnswerWebRTCRefusesBadLocal(t *testing.T) {
	offer, browser := browserOffer(t, "chrome_offer.sdp")
	chosen, _ := browser.Pick("g711")
	audio := AnswerFor(browser, chosen)
	for name, mod := range map[string]func(*WebRTCLocal){
		"no host":        func(l *WebRTCLocal) { l.Hosts = nil },
		"hostname":       func(l *WebRTCLocal) { l.Hosts = []string{"pbx.example"} },
		"ipv6":           func(l *WebRTCLocal) { l.Hosts = []string{"::1"} },
		"no port":        func(l *WebRTCLocal) { l.Port = 0 },
		"short password": func(l *WebRTCLocal) { l.Pwd = "short" },
		"md5 print":      func(l *WebRTCLocal) { l.Fingerprint = "md5 AA:BB" },
		"bad setup":      func(l *WebRTCLocal) { l.Setup = "both" },
	} {
		l := testLocal()
		mod(&l)
		if _, err := AnswerWebRTC(offer, audio, l); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := AnswerWebRTC(offer, AudioSDP{}, testLocal()); err == nil {
		t.Error("an answer with no codec was written")
	}
}

func TestValidateWebRTCRejects(t *testing.T) {
	good := string(corpus(t)["chrome_offer.sdp"])
	cases := map[string]string{
		"plain RTP":       strings.Replace(good, "UDP/TLS/RTP/SAVPF", "RTP/AVP", 1),
		"no rtcp-mux":     strings.Replace(good, "a=rtcp-mux\r\n", "", 1),
		"no ice ufrag":    strings.Replace(good, "a=ice-ufrag:rK7q\r\n", "", 1),
		"short ice pwd":   strings.Replace(good, "a=ice-pwd:b3Rj0cA9kZpLm2Xw8QeVtYu1", "a=ice-pwd:short", 1),
		"no fingerprint":  regexpLine(good, "a=fingerprint:"),
		"md5 fingerprint": strings.Replace(good, "a=fingerprint:sha-256 7B:8B", "a=fingerprint:md5 7B:8B", 1),
		"no setup":        strings.Replace(good, "a=setup:actpass\r\n", "", 1),
		"no mid":          strings.Replace(good, "a=mid:0\r\n", "", 1),
	}
	for name, body := range cases {
		s, err := ParseSDPSession([]byte(body))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err := ValidateWebRTC(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Two active sections must share one BUNDLE group.
	two := strings.Replace(string(corpus(t)["safari_offer.sdp"]), "a=group:BUNDLE 0 1", "a=group:BUNDLE 0", 1)
	s, _ := ParseSDPSession([]byte(two))
	if err := ValidateWebRTC(s); err == nil {
		t.Error("a section outside the BUNDLE group was accepted")
	}
	// A rejected section is not checked.
	rej := strings.Replace(string(corpus(t)["safari_offer.sdp"]), "m=video 9", "m=video 0", 1)
	rej = strings.Replace(rej, "a=group:BUNDLE 0 1", "a=group:BUNDLE 0", 1)
	s, _ = ParseSDPSession([]byte(rej))
	if err := ValidateWebRTC(s); err != nil {
		t.Errorf("rejected video section failed the check: %v", err)
	}
}

func regexpLine(body, prefix string) string {
	var out []string
	for _, l := range strings.Split(body, "\r\n") {
		if !strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\r\n")
}

func TestProfileOf(t *testing.T) {
	for proto, want := range map[string]Profile{
		"RTP/AVP": ProfilePlain, "RTP/SAVP": ProfileSDES,
		"UDP/TLS/RTP/SAVP": ProfileDTLS, "UDP/TLS/RTP/SAVPF": ProfileWebRTC, "udp/tls/rtp/savpf": ProfileWebRTC,
	} {
		got, ok := ProfileOf(proto)
		if !ok || got != want {
			t.Errorf("%s: %q %v", proto, got, ok)
		}
		if ok && got.Proto() != strings.ToUpper(proto) {
			t.Errorf("%s: Proto() %q", proto, got.Proto())
		}
	}
	for _, p := range []string{"RTP/AVPF", "TCP/RTP/AVP", "UDP/DTLS/SCTP", ""} {
		if _, ok := ProfileOf(p); ok {
			t.Errorf("%q is not a relayed transport", p)
		}
	}
	if ProfilePlain.Encrypted() || !ProfileWebRTC.Encrypted() {
		t.Error("Encrypted")
	}
}
