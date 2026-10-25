package sip

import (
	"reflect"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
)

// wideSDP is a phone's SDP with the given m= payload types and attribute
// lines, naming loopback as the media address.
func wideSDP(pts string, attrs ...string) string {
	s := "v=0\r\no=p 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4970 RTP/AVP " + pts + "\r\n"
	for _, a := range attrs {
		s += "a=" + a + "\r\n"
	}
	return s
}

func codecPTs(t *testing.T, body []byte) []uint8 {
	t.Helper()
	a, err := media.ParseAudioSDP(body)
	if err != nil {
		t.Fatalf("not an audio SDP: %v\n%s", err, body)
	}
	var out []uint8
	for _, c := range a.Codecs {
		out = append(out, c.PT)
	}
	return out
}

// TestWidebandNegotiation fails if two phones that both do G.722 do not end
// up on G.722, if the callee is offered anything beyond G.722, Opus, PCMU,
// PCMA and telephone-event, or if the caller is answered with anything but
// what the callee chose.
func TestWidebandNegotiation(t *testing.T) {
	for _, c := range []struct {
		name       string
		calleeSDP  string
		wantAnswer []uint8
	}{
		{"callee picks G.722", wideSDP("9 101", "rtpmap:101 telephone-event/8000"), []uint8{9}},
		{"callee picks PCMU", wideSDP("0 101", "rtpmap:101 telephone-event/8000"), []uint8{0}},
		{"callee picks Opus", wideSDP("96 101", "rtpmap:96 opus/48000/2", "rtpmap:101 telephone-event/8000"), []uint8{96}},
	} {
		t.Run(c.name, func(t *testing.T) {
			pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false))
			a := newPhone(t, pbx, "a1", "pa")
			a.sdp = wideSDP("9 96 0 8 18 101", "rtpmap:96 opus/48000/2", "fmtp:96 useinbandfec=1", "rtpmap:18 G729/8000", "rtpmap:101 telephone-event/8000")
			a.register(t)
			cc := newPhone(t, pbx, "c1", "pc")
			cc.sdp = c.calleeSDP
			cc.register(t)
			cc.setCallee(answerAfter(nil))
			r := waitCall(t, dial(t.Context(), a, "300"))
			if r.err != nil {
				t.Fatalf("call: %v", r.err)
			}
			invC := waitReq(t, cc.invites, "INVITE to c")
			waitReq(t, cc.acks, "ACK to c")
			// Toward the callee: the caller's list, G.729 removed, its order
			// and its Opus parameters intact, on the relay's leg-b port.
			if got := codecPTs(t, invC.Body()); !reflect.DeepEqual(got, []uint8{9, 96, 0, 8}) {
				t.Errorf("callee offered %v, want 9 96 0 8", got)
			}
			offB := anchoredSDP(t, pbx, invC.Body())
			if len(offB.Events) != 1 || offB.DTMFPayloadType != 101 {
				t.Errorf("callee offer events %+v", offB.Events)
			}
			if offB.Codecs[1].Fmtp != "useinbandfec=1" {
				t.Errorf("Opus fmtp %q was not forwarded", offB.Codecs[1].Fmtp)
			}
			// Toward the caller: exactly the callee's choice.
			ans := anchoredSDP(t, pbx, r.dcs.InviteResponse.Body())
			if got := codecPTs(t, r.dcs.InviteResponse.Body()); !reflect.DeepEqual(got, c.wantAnswer) {
				t.Errorf("caller answered %v, want %v", got, c.wantAnswer)
			}
			if ans.DTMFPayloadType != 101 {
				t.Errorf("caller answer lost telephone-event: %d", ans.DTMFPayloadType)
			}
			if offB.Port == ans.Port {
				t.Errorf("both relay legs share port %d", ans.Port)
			}
			hangup(t, r.dcs)
		})
	}
}

// TestHoldKeepsNegotiatedCodec fails if a hold re-INVITE that lists every
// codec again is answered with the whole list instead of the codec the call
// runs on.
func TestHoldKeepsNegotiatedCodec(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false))
	a := newPhone(t, pbx, "a1", "pa")
	a.sdp = wideSDP("9 0 8 101", "rtpmap:101 telephone-event/8000")
	a.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.sdp = wideSDP("9 101", "rtpmap:101 telephone-event/8000")
	c.register(t)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "300"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to c")
	waitReq(t, c.acks, "ACK to c")
	res := a.reinviteAsCaller(t, r.dcs, wideSDP("9 0 8 101", "rtpmap:101 telephone-event/8000", "sendonly"))
	if res.StatusCode != 200 {
		t.Fatalf("hold = %d", res.StatusCode)
	}
	if got := codecPTs(t, res.Body()); !reflect.DeepEqual(got, []uint8{9}) {
		t.Errorf("hold answered with %v, want G.722 only", got)
	}
	peer := waitReq(t, c.reinvites, "hold re-INVITE to c")
	if got := codecPTs(t, peer.Body()); !reflect.DeepEqual(got, []uint8{9}) {
		t.Errorf("the callee's hold re-INVITE lists %v, want G.722 only", got)
	}
	hangup(t, r.dcs)
}

// trunkCall places a call from a phone offering G.722, PCMU and PCMA through
// a trunk with the given codec allow-list and answering SDP, and returns the
// INVITE the carrier received and the SDP the caller was answered with.
func trunkCall(t *testing.T, codecs []string, carrierSDP string) (inv, answer []byte) {
	t.Helper()
	car := newCarrier(t, "acct1", "pw-primary")
	car.sdp = carrierSDP
	tr := car.trunk(1, "carrier-primary", "ip")
	tr.Codecs = codecs
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withRouter(r, nil), withMedia(false))
	a := newPhone(t, pbx, "a1", "pa")
	a.sdp = wideSDP("9 0 8 101", "rtpmap:101 telephone-event/8000")
	a.register(t)
	call := waitCall(t, dial(t.Context(), a, "0501234567"))
	if call.err != nil {
		t.Fatalf("call: %v", call.err)
	}
	got := waitReq(t, car.invites, "INVITE to the carrier")
	waitReq(t, car.acks, "ACK to the carrier")
	ans := call.dcs.InviteResponse.Body()
	hangup(t, call.dcs)
	return got.Body(), ans
}

// TestTrunkCodecAllowList fails if a trunk without a codec list is offered
// G.722, if a trunk the operator widened is not, or if the caller is not
// answered with the carrier's G.711 choice.
func TestTrunkCodecAllowList(t *testing.T) {
	g711 := "v=0\r\no=c 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\n"
	t.Run("default is G.711 only", func(t *testing.T) {
		inv, ans := trunkCall(t, nil, g711)
		if got := codecPTs(t, inv); !reflect.DeepEqual(got, []uint8{0, 8}) {
			t.Errorf("carrier offered %v, want PCMU and PCMA only", got)
		}
		if got := codecPTs(t, ans); !reflect.DeepEqual(got, []uint8{0}) {
			t.Errorf("caller answered %v, want PCMU", got)
		}
	})
	t.Run("widened trunk gets G.722", func(t *testing.T) {
		g722 := "v=0\r\no=c 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 9\r\n"
		inv, ans := trunkCall(t, []string{"G722", "PCMU", "PCMA"}, g722)
		if got := codecPTs(t, inv); !reflect.DeepEqual(got, []uint8{9, 0, 8}) {
			t.Errorf("carrier offered %v, want 9 0 8", got)
		}
		if got := codecPTs(t, ans); !reflect.DeepEqual(got, []uint8{9}) {
			t.Errorf("caller answered %v, want G.722", got)
		}
	})
}

func TestLegCodecs(t *testing.T) {
	if got := legCodecs(&leg{}); got != nil {
		t.Errorf("a phone leg has no filter, got %v", got)
	}
	if got := legCodecs(&leg{trunk: &routing.Trunk{}}); !reflect.DeepEqual(got, media.DefaultTrunkCodecs) {
		t.Errorf("default trunk codecs %v", got)
	}
	if got := legCodecs(&leg{trunk: &routing.Trunk{Codecs: []string{"G722"}}}); !reflect.DeepEqual(got, []string{"G722"}) {
		t.Errorf("trunk codecs %v", got)
	}
}

// A wideband-only caller to a G.711-only trunk: the filter would leave
// nothing, so the caller's list goes out and the carrier decides.
func TestFilteredOfferFallsBack(t *testing.T) {
	off, err := media.ParseAudioSDP([]byte(wideSDP("9 101", "rtpmap:101 telephone-event/8000")))
	if err != nil {
		t.Fatal(err)
	}
	if got := filteredOffer(off, media.DefaultTrunkCodecs); len(got.Codecs) != 1 || got.Codecs[0].PT != 9 {
		t.Errorf("fallback offer %+v", got.Codecs)
	}
}
