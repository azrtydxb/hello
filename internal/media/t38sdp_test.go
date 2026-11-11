package media

import (
	"strings"
	"testing"
)

const carrierT38Offer = "v=0\r\no=carrier 1 1 IN IP4 203.0.113.9\r\ns=-\r\nc=IN IP4 203.0.113.9\r\nt=0 0\r\n" +
	"m=image 17000 udptl t38\r\na=T38FaxVersion:0\r\na=T38MaxBitRate:9600\r\na=T38FaxRateManagement:transferredTCF\r\na=T38FaxUdpEC:t38UDPRedundancy\r\n"

func TestParseT38SDP(t *testing.T) {
	got, ok := ParseT38SDP([]byte(carrierT38Offer))
	if !ok || got.Port != 17000 || got.Address != "203.0.113.9" || got.MaxBitRate != 9600 ||
		got.RateManagement != "transferredTCF" || got.UDPEC != "t38UDPRedundancy" {
		t.Fatalf("%+v ok=%v", got, ok)
	}
	if !got.T38Acceptable() || !OffersT38([]byte(carrierT38Offer)) {
		t.Fatal("a standard offer is acceptable")
	}
	// Audio and image together: the image stream is found after the audio one.
	both := "v=0\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\nm=image 4002 udptl t38\r\na=T38FaxUdpEC:t38UDPRedundancy\r\n"
	if g, ok := ParseT38SDP([]byte(both)); !ok || g.Port != 4002 || g.Address != "10.0.0.1" {
		t.Fatalf("mixed offer: %+v %v", g, ok)
	}
	// Audio only: not T.38.
	if _, ok := ParseT38SDP([]byte("v=0\r\nc=IN IP4 10.0.0.1\r\nm=audio 4000 RTP/AVP 0\r\n")); ok {
		t.Fatal("an audio SDP read as T.38")
	}
	// A refused stream is distinguishable from absence.
	g, ok := ParseT38SDP([]byte(strings.Replace(carrierT38Offer, "17000", "0", 1)))
	if !ok || g.Port != 0 || OffersT38([]byte(strings.Replace(carrierT38Offer, "17000", "0", 1))) {
		t.Fatalf("refused stream: %+v %v", g, ok)
	}
}

func TestT38Acceptable(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*T38SDP)
		want bool
	}{
		{"standard", func(*T38SDP) {}, true},
		{"local TCF is not served", func(s *T38SDP) { s.RateManagement = "localTCF" }, false},
		{"FEC is not served", func(s *T38SDP) { s.UDPEC = "t38UDPFEC" }, false},
		{"version 1 is not served", func(s *T38SDP) { s.Version = 1 }, false},
		{"refused", func(s *T38SDP) { s.Port = 0 }, false},
	} {
		s := T38SDP{Port: 4000, MaxBitRate: 14400, RateManagement: "transferredTCF", UDPEC: "t38UDPRedundancy"}
		tc.mod(&s)
		if s.T38Acceptable() != tc.want {
			t.Errorf("%s: acceptable = %v", tc.name, !tc.want)
		}
	}
}

func TestBuildT38SDP(t *testing.T) {
	body := string(BuildT38SDP("198.51.100.7", 20010))
	for _, want := range []string{
		"c=IN IP4 198.51.100.7", "m=image 20010 udptl t38", "a=T38FaxVersion:0", "a=T38MaxBitRate:14400",
		"a=T38FaxRateManagement:transferredTCF", "a=T38FaxUdpEC:t38UDPRedundancy",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("answer lacks %q:\n%s", want, body)
		}
	}
	back, ok := ParseT38SDP([]byte(body))
	if !ok || back.Port != 20010 || !back.T38Acceptable() {
		t.Fatalf("our own SDP does not parse back: %+v", back)
	}
}
