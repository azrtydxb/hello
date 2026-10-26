package media

import (
	"reflect"
	"strings"
	"testing"
)

// sdp builds a test SDP body from m= line payload types and attribute lines.
func sdp(pts string, attrs ...string) []byte {
	b := "v=0\r\no=- 1 1 IN IP4 10.0.0.9\r\ns=-\r\nc=IN IP4 10.0.0.9\r\nt=0 0\r\nm=audio 49170 RTP/AVP " + pts + "\r\n"
	for _, a := range attrs {
		b += "a=" + a + "\r\n"
	}
	return []byte(b)
}

func mustParse(t *testing.T, b []byte) AudioSDP {
	t.Helper()
	a, err := ParseAudioSDP(b)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, b)
	}
	return a
}

func names(cs []Codec) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func pts(cs []Codec) []uint8 {
	var out []uint8
	for _, c := range cs {
		out = append(out, c.PT)
	}
	return out
}

// A phone offering everything: the filter keeps G.722, Opus, PCMU, PCMA and
// telephone-event in the caller's order, drops the rest, and the rtpmap and
// fmtp lines survive.
func TestFilterMixedOffer(t *testing.T) {
	offer := mustParse(t, sdp("9 96 0 8 18 97 101",
		"rtpmap:96 opus/48000/2",
		"fmtp:96 minptime=10;useinbandfec=1",
		"rtpmap:18 G729/8000",
		"fmtp:18 annexb=no",
		"rtpmap:97 iLBC/8000",
		"rtpmap:101 telephone-event/8000",
		"fmtp:101 0-16",
	))
	if got := names(offer.Codecs); !reflect.DeepEqual(got, []string{"G722", "opus", "PCMU", "PCMA", "G729", "iLBC"}) {
		t.Fatalf("parsed codecs %v", got)
	}
	f := offer.Filter(PhoneCodecs)
	if got := pts(f.Codecs); !reflect.DeepEqual(got, []uint8{9, 96, 0, 8}) {
		t.Fatalf("filtered payload types %v, want 9 96 0 8 in the caller's order", got)
	}
	body := string(f.Build("192.0.2.1", 30000, "sendrecv"))
	for _, want := range []string{
		"m=audio 30000 RTP/AVP 9 96 0 8 101\r\n",
		"a=rtpmap:9 G722/8000\r\n", // G.722's RTP clock is 8000 although the audio is 16 kHz
		"a=rtpmap:96 opus/48000/2\r\n",
		"a=fmtp:96 minptime=10;useinbandfec=1\r\n",
		"a=rtpmap:0 PCMU/8000\r\n",
		"a=rtpmap:8 PCMA/8000\r\n",
		"a=rtpmap:101 telephone-event/8000\r\n",
		"a=fmtp:101 0-16\r\n",
		"c=IN IP4 192.0.2.1\r\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("offer missing %q in:\n%s", want, body)
		}
	}
	for _, bad := range []string{"G729", "iLBC", "annexb"} {
		if strings.Contains(body, bad) {
			t.Errorf("offer still names %q:\n%s", bad, body)
		}
	}
	// What we wrote parses back to the same codec set.
	again := mustParse(t, []byte(body))
	if !reflect.DeepEqual(again.Codecs, f.Codecs) {
		t.Errorf("round trip changed the codecs:\n got %+v\nwant %+v", again.Codecs, f.Codecs)
	}
}

// A G.711-only trunk is offered PCMU and PCMA only, in the caller's order,
// whatever wideband the phone offered.
func TestFilterG711OnlyTrunk(t *testing.T) {
	offer := mustParse(t, sdp("9 96 8 0 101", "rtpmap:96 opus/48000/2", "rtpmap:101 telephone-event/8000"))
	f := offer.Filter(DefaultTrunkCodecs)
	if got := pts(f.Codecs); !reflect.DeepEqual(got, []uint8{8, 0}) {
		t.Fatalf("trunk offer %v, want PCMA then PCMU as the caller ordered", got)
	}
	if f.PayloadType != 8 || f.DTMFPayloadType != 101 {
		t.Errorf("summary pt %d dtmf %d", f.PayloadType, f.DTMFPayloadType)
	}
	// A trunk the operator widened gets G.722 too.
	f = offer.Filter([]string{"PCMU", "PCMA", "G722"})
	if got := pts(f.Codecs); !reflect.DeepEqual(got, []uint8{9, 8, 0}) {
		t.Errorf("widened trunk offer %v", got)
	}
	// Names are matched without regard to case.
	if got := pts(offer.Filter([]string{"OPUS"}).Codecs); !reflect.DeepEqual(got, []uint8{96}) {
		t.Errorf("opus-only filter %v", got)
	}
}

// A wideband-only caller leaves a G.711-only trunk nothing to be offered.
func TestFilterWidebandOnlyToG711Trunk(t *testing.T) {
	offer := mustParse(t, sdp("9 101", "rtpmap:101 telephone-event/8000"))
	f := offer.Filter(DefaultTrunkCodecs)
	if len(f.Codecs) != 0 || len(f.Events) != 0 || f.PayloadType != 0 {
		t.Fatalf("filter left %+v", f)
	}
	if offer.Filter(nil).PayloadType != 9 {
		t.Error("no allow-list must keep G.722")
	}
}

// Dynamic payload types: the offerer's numbering is kept end to end and the
// answer is matched by number and name.
func TestDynamicPayloadTypes(t *testing.T) {
	offer := mustParse(t, sdp("111 0 126", "rtpmap:111 opus/48000/2", "fmtp:111 useinbandfec=1", "rtpmap:126 telephone-event/48000"))
	if offer.PayloadType != 111 || offer.Codec.Name != "opus" || offer.Codec.ClockRate != 48000 || offer.Codec.Channels != 2 {
		t.Fatalf("primary codec %+v", offer.Codec)
	}
	if offer.DTMFPayloadType != 126 || offer.DTMFRate != 48000 {
		t.Errorf("dtmf %d/%d", offer.DTMFPayloadType, offer.DTMFRate)
	}
	// The callee answers with Opus on the caller's number.
	callee := mustParse(t, sdp("111 126", "rtpmap:111 opus/48000/2", "rtpmap:126 telephone-event/48000"))
	ans := AnswerFor(offer, callee)
	if got := pts(ans.Codecs); !reflect.DeepEqual(got, []uint8{111}) {
		t.Fatalf("answer payload types %v", got)
	}
	if ans.Codecs[0].Fmtp != "useinbandfec=1" {
		t.Errorf("the caller's own fmtp must be answered: %q", ans.Codecs[0].Fmtp)
	}
	body := string(ans.Build("192.0.2.1", 30002, "sendrecv"))
	if !strings.Contains(body, "m=audio 30002 RTP/AVP 111 126\r\n") || !strings.Contains(body, "a=rtpmap:111 opus/48000/2\r\n") || !strings.Contains(body, "a=rtpmap:126 telephone-event/48000\r\n") {
		t.Errorf("answer:\n%s", body)
	}
	// A callee that answers a number with another codec is not trusted.
	liar := mustParse(t, sdp("111 0", "rtpmap:111 speex/16000"))
	if got := AnswerFor(offer, liar); got.PayloadType != 0 || len(got.Codecs) != 1 {
		t.Errorf("answer %+v, want PCMU only", got)
	}
}

// The callee's choice is the caller's answer: PCMU out of a four-codec
// offer, and the telephone-event the callee kept.
func TestAnswerForCalleeChoice(t *testing.T) {
	offer := mustParse(t, sdp("9 96 0 8 101", "rtpmap:96 opus/48000/2", "rtpmap:101 telephone-event/8000"))
	callee := mustParse(t, sdp("0 101", "rtpmap:101 telephone-event/8000"))
	ans := AnswerFor(offer, callee)
	if got := pts(ans.Codecs); !reflect.DeepEqual(got, []uint8{0}) {
		t.Fatalf("answer %v, want PCMU only", got)
	}
	if ans.PayloadType != 0 || ans.DTMFPayloadType != 101 {
		t.Errorf("summary %d/%d", ans.PayloadType, ans.DTMFPayloadType)
	}
	// A callee that answers more than one codec keeps its order.
	both := mustParse(t, sdp("8 9", "rtpmap:8 PCMA/8000"))
	if got := pts(AnswerFor(offer, both).Codecs); !reflect.DeepEqual(got, []uint8{8, 9}) {
		t.Errorf("two-codec answer %v", got)
	}
	// A callee without telephone-event: the caller is not told to send it.
	noDTMF := mustParse(t, sdp("9"))
	if got := AnswerFor(offer, noDTMF); len(got.Events) != 0 || got.DTMFPayloadType != 0 {
		t.Errorf("events %v", got.Events)
	}
}

// With no callee answer to go on (a call Hello answers itself), the caller
// gets G.722 when it offered it and the first G.711 otherwise.
func TestAnswerForWithoutCallee(t *testing.T) {
	wide := mustParse(t, sdp("0 9 8 101", "rtpmap:101 telephone-event/8000"))
	if got := AnswerFor(wide, AudioSDP{}); got.PayloadType != 9 {
		t.Errorf("wide offer answered with %d, want 9", got.PayloadType)
	}
	narrow := mustParse(t, sdp("8 0"))
	if got := AnswerFor(narrow, AudioSDP{}); got.PayloadType != 8 {
		t.Errorf("narrow offer answered with %d, want the first G.711 (8)", got.PayloadType)
	}
	opusOnly := mustParse(t, sdp("96", "rtpmap:96 opus/48000/2"))
	if got := AnswerFor(opusOnly, AudioSDP{}); OpusAvailable && got.PayloadType != 96 {
		t.Errorf("opus-only offer answered with %d", got.PayloadType)
	}
}

// telephone-event and G.722's clock. The RTP clock of G.722 is 8000 although
// its audio is 16 kHz (RFC 3551 section 4.5.2), so the matching event codec
// is the 8000 one; a phone that offers only telephone-event/16000 is
// answered with what it offered, and the rtpmap of G.722 itself stays 8000.
func TestTelephoneEventForG722(t *testing.T) {
	g722 := Codec{PT: 9, Name: "G722"}
	if g722.SampleRate() != 16000 {
		t.Errorf("G.722 audio rate %d, want 16000", g722.SampleRate())
	}
	c, _ := StaticCodec(9)
	if c.ClockRate != 8000 || c.TimestampStep() != 160 {
		t.Errorf("G.722 clock %d step %d, want 8000 and 160 per 20 ms", c.ClockRate, c.TimestampStep())
	}
	if o := (Codec{PT: 96, Name: "opus", ClockRate: 48000}); o.TimestampStep() != 960 || o.SampleRate() != 16000 {
		t.Errorf("opus step %d rate %d", o.TimestampStep(), o.SampleRate())
	}

	both := mustParse(t, sdp("9 0 101 102", "rtpmap:101 telephone-event/16000", "rtpmap:102 telephone-event/8000"))
	if both.DTMFPayloadType != 102 || both.DTMFRate != 8000 {
		t.Errorf("G.722 with two event rates picked %d/%d, want 102/8000", both.DTMFPayloadType, both.DTMFRate)
	}
	term, ok := both.Terminated()
	if !ok || term.PayloadType != 9 || term.DTMFPayloadType != 102 || len(term.Events) != 1 {
		t.Fatalf("terminated %+v", term)
	}
	body := string(term.Build("192.0.2.1", 31000, "sendrecv"))
	if !strings.Contains(body, "a=rtpmap:9 G722/8000\r\n") || !strings.Contains(body, "a=rtpmap:102 telephone-event/8000\r\n") || strings.Contains(body, "/16000") {
		t.Errorf("answer:\n%s", body)
	}

	only16 := mustParse(t, sdp("9 101", "rtpmap:101 telephone-event/16000"))
	if only16.DTMFPayloadType != 101 || only16.DTMFRate != 16000 {
		t.Errorf("a lone telephone-event/16000 must still be used: %d/%d", only16.DTMFPayloadType, only16.DTMFRate)
	}
	term, _ = only16.Terminated()
	body = string(term.Build("192.0.2.1", 31000, "sendrecv"))
	if !strings.Contains(body, "a=rtpmap:101 telephone-event/16000\r\n") || !strings.Contains(body, "a=rtpmap:9 G722/8000\r\n") {
		t.Errorf("answer for a 16000-only phone:\n%s", body)
	}

	// Opus pairs with the 48000 event.
	op := mustParse(t, sdp("111 126 127", "rtpmap:111 opus/48000/2", "rtpmap:126 telephone-event/8000", "rtpmap:127 telephone-event/48000"))
	if op.DTMFPayloadType != 127 || op.DTMFRate != 48000 {
		t.Errorf("opus event %d/%d, want 127/48000", op.DTMFPayloadType, op.DTMFRate)
	}
}

// Hello-terminated media picks Opus when this build has it and the offer
// lists it, then G.722, then G.711 in the offer's order.
func TestTerminatedPreference(t *testing.T) {
	opus := []string{"rtpmap:96 opus/48000/2"}
	for _, c := range []struct {
		name     string
		pts      string
		attr     []string
		want     uint8
		wantNoOp uint8 // the answer in a build without Opus (0: same as want)
	}{
		{"g722 offered", "0 8 9", nil, 9, 0},
		{"g711 only, pcma first", "8 0", nil, 8, 0},
		{"opus and pcmu", "96 0", opus, 96, 0},
		{"opus after g722", "9 96 0", opus, 96, 9},
		{"opus first of everything", "96 9 0 8", opus, 96, 9},
		{"opus only", "96", opus, 96, 96},
	} {
		want := c.want
		if !OpusAvailable {
			if c.wantNoOp == 96 {
				continue // nothing Hello can answer with
			}
			if c.wantNoOp != 0 {
				want = c.wantNoOp
			} else if want == 96 {
				want = 0
			}
		}
		got, ok := mustParse(t, sdp(c.pts, c.attr...)).Terminated()
		if !ok || got.PayloadType != want || len(got.Codecs) != 1 {
			t.Errorf("%s: got %d (ok %v, %d codecs), want %d", c.name, got.PayloadType, ok, len(got.Codecs), want)
		}
	}
}

// An Opus answer carries the 48000 telephone-event and nothing else.
func TestTerminatedOpusEvents(t *testing.T) {
	if !OpusAvailable {
		t.Skip("no Opus in this build")
	}
	offer := mustParse(t, sdp("9 111 126 127", "rtpmap:111 opus/48000/2", "rtpmap:126 telephone-event/8000", "rtpmap:127 telephone-event/48000"))
	term, ok := offer.Terminated()
	if !ok || term.PayloadType != 111 || term.DTMFPayloadType != 127 || term.DTMFRate != 48000 {
		t.Fatalf("terminated %+v", term)
	}
	body := string(term.Build("192.0.2.1", 31000, "sendrecv"))
	if !strings.Contains(body, "m=audio 31000 RTP/AVP 111 127\r\n") || !strings.Contains(body, "a=rtpmap:127 telephone-event/48000\r\n") {
		t.Errorf("answer:\n%s", body)
	}
}

// Narrow keeps one codec for a re-INVITE, falling back to the first
// supported one when the offer dropped it.
func TestNarrow(t *testing.T) {
	offer := mustParse(t, sdp("9 0 8 101", "rtpmap:101 telephone-event/8000"))
	if got := offer.Narrow(0); got.PayloadType != 0 || len(got.Codecs) != 1 || got.DTMFPayloadType != 101 {
		t.Errorf("narrow to PCMU: %+v", got)
	}
	if got := offer.Narrow(96); got.PayloadType != 9 || len(got.Codecs) != 1 {
		t.Errorf("narrow to a codec not on offer: %+v", got)
	}
}

func TestParseSkipsUnknownAndKeepsStatic(t *testing.T) {
	// No rtpmap for the static types: RFC 3551 fills them in.
	a := mustParse(t, sdp("18 9 0"))
	if got := names(a.Codecs); !reflect.DeepEqual(got, []string{"G722", "PCMU"}) {
		t.Errorf("codecs %v (G.729 without an rtpmap is unknown and skipped)", got)
	}
	if _, err := ParseAudioSDP(sdp("18 97", "rtpmap:97 iLBC/8000")); err == nil {
		t.Error("an offer with no codec Hello can use must be refused")
	}
}

func TestBuildAudioSDPG722(t *testing.T) {
	body := string(BuildAudioSDP("192.0.2.1", 30000, 9, 101, 8000))
	if !strings.Contains(body, "m=audio 30000 RTP/AVP 9 101\r\n") || !strings.Contains(body, "a=rtpmap:9 G722/8000\r\n") {
		t.Errorf("sdp:\n%s", body)
	}
	if got := SDPDirection([]byte(string(BuildAudioSDPDir("192.0.2.1", 30000, 9, 0, 0, "sendonly")))); got != "sendonly" {
		t.Errorf("direction %q", got)
	}
}
