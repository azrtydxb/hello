package sip

import (
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// doorOpenTestCode is the stored code of the test station. It has no run of
// digits that could occur by chance in an ID or timestamp.
const doorOpenTestCode = "8#42"

// doorOpenFeatures is doorFeatures plus a door-open code on station 900.
func doorOpenFeatures(hangup bool) pbxOpt {
	base := doorFeatures()
	return func(c *Config, d *Deps) {
		base(c, d)
		d.Snapshots.(*fakeSnaps).p.Load().WithDoorOpen(map[string]snapshot.DoorOpenSetting{
			"900": {Code: doorOpenTestCode, Hangup: hangup},
		})
		c.DoorHangup = 100 * time.Millisecond
	}
}

// rtpSink listens where a phone's SDP says it receives media and records
// the telephone events it gets, one entry per press, in order.
type rtpSink struct {
	conn net.PacketConn
	mu   sync.Mutex
	seen map[[2]uint32]bool
	keys []byte
}

func newRTPSink(t *testing.T, p *phone) *rtpSink {
	t.Helper()
	// The phone's own media port is its signalling port + 1000, which can
	// fall past 65535; take a free one and put it in the phone's SDP.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	port := conn.LocalAddr().(*net.UDPAddr).Port
	text := p.rewriteSDP(func(s string) string {
		return regexp.MustCompile(`m=audio \d+`).ReplaceAllString(s, "m=audio "+strconv.Itoa(port))
	})
	sdp, err := media.ParseAudioSDP([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	s := &rtpSink{conn: conn, seen: map[[2]uint32]bool{}}
	go s.run(sdp.DTMFPayloadType)
	return s
}

func (s *rtpSink) run(dtmfPT uint8) {
	buf := make([]byte, 2048)
	for {
		n, _, err := s.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		pkt, ok := media.ParseRTP(buf[:n])
		if !ok || pkt.PayloadType != dtmfPT {
			continue
		}
		ev, ok := media.ParseTelephoneEvent(pkt.Payload)
		if !ok {
			continue
		}
		id := [2]uint32{pkt.SSRC, pkt.Timestamp}
		s.mu.Lock()
		if !s.seen[id] {
			s.seen[id] = true
			s.keys = append(s.keys, ev.Code)
		}
		s.mu.Unlock()
	}
}

func (s *rtpSink) pressed() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.keys)
}

func (s *rtpSink) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasSuffix(s.pressed(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the sink got presses %q, want them to end with %q", s.pressed(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDoorOpenDTMF fails if *50 on a door call does not send the stored code
// as RFC 2833 events to the door leg, after the keys the user dialled; if the
// code appears in the trace or CDR; if *50 from the door's own side or on an
// ordinary call does anything but pass through; or if hang-up-after-open is
// not honoured (spec S-47).
func TestDoorOpenDTMF(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorOpenFeatures(true))
	user := newPhone(t, pbx, "a1", "pa")
	user.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	door := newPhone(t, pbx, "d1", "pd")
	door.register(t)

	t.Run("answering user opens the door", func(t *testing.T) {
		doorSink := newRTPSink(t, door)
		r := waitCall(t, dial(t.Context(), door, "800"))
		if r.err != nil {
			t.Fatalf("door call: %v", r.err)
		}
		inv := waitReq(t, b.invites, "INVITE to the answering user")
		waitReq(t, b.acks, "ACK to b")
		feed := startRTPFeed(t, inv.Body())
		feed.digit(10) // *
		feed.digit(5)
		feed.digit(0)
		// The keys pass through, then the stored code follows them: 8 # 4 2.
		doorSink.waitFor(t, "*50"+doorOpenTestCode)
		// Hang-up after open ends both legs.
		waitReq(t, b.byes, "BYE to the answering user")
		waitReq(t, door.byes, "BYE to the door station")
		cd := pbx.nextCDR(t)
		raw, _ := json.Marshal(cd)
		if strings.Contains(string(raw), doorOpenTestCode) {
			t.Error("the door-open code is in the CDR")
		}
		wantTrace(t, cd.Trace, "Door open pressed (key)")
		wantTrace(t, cd.Trace, "Door open code sent")
	})

	t.Run("the station's own keys open nothing", func(t *testing.T) {
		bSink := newRTPSink(t, b)
		r := waitCall(t, dial(t.Context(), door, "800"))
		if r.err != nil {
			t.Fatalf("door call: %v", r.err)
		}
		waitReq(t, b.invites, "INVITE to the answering user")
		waitReq(t, b.acks, "ACK to b")
		feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
		feed.digit(10)
		feed.digit(5)
		feed.digit(0)
		bSink.waitFor(t, "*50")
		time.Sleep(500 * time.Millisecond)
		if got := bSink.pressed(); strings.Contains(got, "#") || strings.Contains(got, "42") {
			t.Errorf("the user heard %q: the door's own *50 sent the code", got)
		}
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE to b")
		cd := pbx.nextCDR(t)
		for _, st := range cd.Trace {
			if strings.Contains(st.Text, "Door open") {
				t.Errorf("trace %q: the door's own keys opened its door", st.Text)
			}
		}
	})

	t.Run("an ordinary call passes *50 through", func(t *testing.T) {
		aSink := newRTPSink(t, user)
		r := waitCall(t, dial(t.Context(), user, "200"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		inv := waitReq(t, b.invites, "INVITE to b")
		waitReq(t, b.acks, "ACK to b")
		feed := startRTPFeed(t, inv.Body())
		feed.digit(10)
		feed.digit(5)
		feed.digit(0)
		aSink.waitFor(t, "*50")
		time.Sleep(500 * time.Millisecond)
		if got := aSink.pressed(); got != "*50" {
			t.Errorf("the caller heard %q, want only the keys *50", got)
		}
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE to b")
		cd := pbx.nextCDR(t)
		for _, st := range cd.Trace {
			if strings.Contains(st.Text, "Door open") {
				t.Errorf("trace %q on an ordinary call", st.Text)
			}
		}
	})
}

// TestDoorOpenWithoutCode fails if *50 on a door call whose station has no
// code does anything, or if hang-up-after-open is applied when the station is
// not set to hang up.
func TestDoorOpenWithoutCode(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorOpenFeatures(false))
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	door := newPhone(t, pbx, "d1", "pd")
	door.register(t)
	doorSink := newRTPSink(t, door)
	r := waitCall(t, dial(t.Context(), door, "800"))
	if r.err != nil {
		t.Fatalf("door call: %v", r.err)
	}
	inv := waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")
	feed := startRTPFeed(t, inv.Body())
	feed.digit(10)
	feed.digit(5)
	feed.digit(0)
	doorSink.waitFor(t, "*50"+doorOpenTestCode)
	noReq(t, b.byes, 600*time.Millisecond, "a BYE: the station is not set to hang up after open")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestDoorOpenByAPI fails if the command the API queues (POST
// /calls/{id}/door-open) opens the door for an extension that did not answer
// the call, fails to send the code for the one that did, or if the live call
// does not name its door station (spec S-47).
func TestDoorOpenByAPI(t *testing.T) {
	park := newMemPark()
	opt := doorOpenFeatures(false)
	pbx := startPBX(t, doorDevices(), withMedia(false), opt, func(c *Config, d *Deps) { d.Park = park })
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	door := newPhone(t, pbx, "d1", "pd")
	door.register(t)
	doorSink := newRTPSink(t, door)
	r := waitCall(t, dial(t.Context(), door, "800"))
	if r.err != nil {
		t.Fatalf("door call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")

	var id string
	eventually(t, "a connected door call in the live state", func() bool {
		for _, c := range liveCalls(t, pbx) {
			if c.Door == "900" && c.State == "connected" && c.Callee == "200" {
				id = c.ID
				return true
			}
		}
		return false
	})

	asked := 0
	ask := func(ext string) (rep livestate.ParkReply) {
		t.Helper()
		asked++
		cmdID := "cmd-" + ext + "-" + strconv.Itoa(asked)
		if err := park.ParkSend(t.Context(), pbx.cfg.NodeID, livestate.ParkCommand{
			ID: cmdID, Op: livestate.ParkOpDoorOpen, Correlation: id, Retriever: ext}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the reply to the door-open command", func() bool {
			var ok bool
			rep, ok, _ = park.ParkGetReply(t.Context(), cmdID)
			return ok
		})
		return rep
	}
	if rep := ask("100"); rep.OK || rep.Code != 403 {
		t.Errorf("an extension that did not answer got %+v, want 403", rep)
	}
	if got := doorSink.pressed(); got != "" {
		t.Errorf("the door heard %q after a refused request", got)
	}
	if rep := ask("200"); !rep.OK {
		t.Fatalf("the answering extension got %+v, want ok", rep)
	}
	doorSink.waitFor(t, doorOpenTestCode)
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	wantTrace(t, cd.Trace, "Door open pressed (api)")

	if rep := ask("200"); rep.OK || rep.Code != 404 {
		t.Errorf("a command for an ended call got %+v, want 404", rep)
	}
}
