package media

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAudioSDP(t *testing.T) {
	t.Run("PCMU first and telephone-event", func(t *testing.T) {
		offer := []byte("v=0\r\n" +
			"o=- 1 1 IN IP4 10.0.0.9\r\n" +
			"s=-\r\n" +
			"c=IN IP4 10.0.0.9\r\n" +
			"t=0 0\r\n" +
			"m=audio 49170 RTP/AVP 0 8 101\r\n" +
			"a=rtpmap:8 PCMA/8000\r\n" +
			"a=rtpmap:101 telephone-event/8000\r\n")
		got, err := ParseAudioSDP(offer)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Address != "10.0.0.9" || got.Port != 49170 {
			t.Errorf("address %q port %d", got.Address, got.Port)
		}
		// Payload type 0 is listed first in m=, so it wins over 8.
		if got.PayloadType != 0 {
			t.Errorf("payload type %d, want 0 (first in the offer)", got.PayloadType)
		}
		if got.DTMFPayloadType != 101 || got.DTMFRate != 8000 {
			t.Errorf("dtmf pt %d rate %d", got.DTMFPayloadType, got.DTMFRate)
		}
	})
	t.Run("PCMA when listed first", func(t *testing.T) {
		offer := []byte("v=0\r\nc=IN IP4 10.0.0.9\r\n" +
			"m=audio 5004 RTP/AVP 8 0 101\r\n" +
			"a=rtpmap:101 telephone-event/4000\r\n")
		got, err := ParseAudioSDP(offer)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.PayloadType != 8 {
			t.Errorf("payload type %d, want 8", got.PayloadType)
		}
		if got.DTMFRate != 4000 {
			t.Errorf("dtmf rate %d, want 4000 as offered", got.DTMFRate)
		}
	})
	t.Run("telephone-event without rate defaults to 8000", func(t *testing.T) {
		offer := []byte("c=IN IP4 1.2.3.4\r\nm=audio 6000 RTP/AVP 0 100\r\na=rtpmap:100 telephone-event\r\n")
		got, err := ParseAudioSDP(offer)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.DTMFPayloadType != 100 || got.DTMFRate != sampleRateHz {
			t.Errorf("dtmf pt %d rate %d", got.DTMFPayloadType, got.DTMFRate)
		}
	})
	t.Run("media-level c= overrides session-level", func(t *testing.T) {
		offer := []byte("c=IN IP4 1.2.3.4\r\n" +
			"m=audio 7000 RTP/AVP 0\r\n" +
			"c=IN IP4 5.6.7.8\r\n")
		got, err := ParseAudioSDP(offer)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Address != "5.6.7.8" {
			t.Errorf("address %q, want the media-level one", got.Address)
		}
	})
	t.Run("hostile shapes", func(t *testing.T) {
		cases := []struct {
			name  string
			offer string
		}{
			{"empty", ""},
			{"port zero", "c=IN IP4 1.2.3.4\r\nm=audio 0 RTP/AVP 0\r\n"},
			{"no supported codec", "c=IN IP4 1.2.3.4\r\nm=audio 5000 RTP/AVP 9 96\r\n"},
			{"no audio section", "c=IN IP4 1.2.3.4\r\nm=video 5000 RTP/AVP 96\r\n"},
			{"no c line", "m=audio 5000 RTP/AVP 0\r\n"},
			{"ip6 c line", "c=IN IP6 ::1\r\nm=audio 5000 RTP/AVP 0\r\n"},
			{"bad port", "c=IN IP4 1.2.3.4\r\nm=audio banana RTP/AVP 0\r\n"},
			{"bad dtmf rate", "c=IN IP4 1.2.3.4\r\nm=audio 5000 RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/loud\r\n"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if _, err := ParseAudioSDP([]byte(c.offer)); err == nil {
					t.Fatal("parse succeeded, want error")
				}
			})
		}
	})
	// Junk lines around the SDP must be skipped without failing.
	t.Run("junk lines skipped", func(t *testing.T) {
		offer := "hello there\n" + strings.ReplaceAll(
			"c=IN IP4 1.2.3.4\r\nm=audio 5000 RTP/AVP 0\r\n", "\r\n", "\r\n") + "\n"
		got, err := ParseAudioSDP([]byte(offer))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Port != 5000 {
			t.Errorf("port %d, want 5000", got.Port)
		}
	})
}

func TestBuildAudioSDP(t *testing.T) {
	answer := BuildAudioSDP("192.168.1.10", 30000, 8, 101, 8000)
	text := string(answer)
	for _, want := range []string{
		"v=0\r\n",
		"s=-\r\n",
		"c=IN IP4 192.168.1.10\r\n",
		"t=0 0\r\n",
		"m=audio 30000 RTP/AVP 8 101\r\n",
		"a=rtpmap:8 PCMA/8000\r\n",
		"a=rtpmap:101 telephone-event/8000\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answer missing %q", want)
		}
	}
	if !bytes.HasSuffix(answer, []byte("\r\n")) {
		t.Error("answer must end with a CRLF")
	}
	// Without DTMF the m= line carries only the codec.
	noDTMF := string(BuildAudioSDP("192.168.1.10", 30000, 0, 0, 8000))
	if strings.Contains(noDTMF, "telephone-event") {
		t.Error("dtmfPT 0 must not advertise telephone-event")
	}
	if !strings.Contains(noDTMF, "m=audio 30000 RTP/AVP 0\r\n") {
		t.Errorf("m= line %q", noDTMF)
	}
}
