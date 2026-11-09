package sip

import (
	"context"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// audioSink counts the audio RTP packets a phone gets at its SDP port.
type audioSink struct {
	conn net.PacketConn
	mu   sync.Mutex
	n    int
}

func newAudioSink(t *testing.T, p *phone) *audioSink {
	t.Helper()
	// The phone's own media port is its signalling port + 1000, which can
	// fall past 65535; take a free one and put it in the phone's SDP.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	port := conn.LocalAddr().(*net.UDPAddr).Port
	p.sdp = regexp.MustCompile(`m=audio \d+`).ReplaceAllString(p.sdp, "m=audio "+strconv.Itoa(port))
	s := &audioSink{conn: conn}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if pkt, ok := media.ParseRTP(buf[:n]); ok && pkt.PayloadType == 0 {
				s.mu.Lock()
				s.n++
				s.mu.Unlock()
			}
		}
	}()
	return s
}

func (s *audioSink) packets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// doorHookFeatures installs a door station 900, the Door group 800 with
// members 200 and 300, an ordinary group 810 and the paging group *60 (200
// and 300).
func doorHookFeatures(dnd ...string) pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) {
		exts := []snapshot.Extension{
			{Number: "100", Name: "Ext 100"}, {Number: "200", Name: "Ext 200"}, {Number: "300", Name: "Ext 300"},
			{Number: "900", Name: "Front door", Door: true},
		}
		for i := range exts {
			exts[i].DND = len(dnd) > 0 && exts[i].Number == dnd[0]
		}
		s.WithExtensionData(exts)
		m := []snapshot.RingGroupMember{{ExtensionID: 2, Extension: "200", Position: 1, Weight: 1}, {ExtensionID: 3, Extension: "300", Position: 2, Weight: 1}}
		s.WithRingGroups([]snapshot.RingGroup{
			{ID: 1, Name: "800", Strategy: "ring-all", RingTimeout: 5, IsDoor: true, RingTag: "doorbell"},
			{ID: 2, Name: "810", Strategy: "ring-all", RingTimeout: 5},
		}, [][]snapshot.RingGroupMember{m, m})
		s.WithPagingGroups([]snapshot.PagingGroup{{ID: 1, Name: "Hall", Code: "*60", Priority: "normal", Enabled: true, Members: []string{"200", "300"}}})
	})
}

// fireDoorTrigger runs a trigger on the PBX and returns when it finished.
func fireDoorTrigger(t *testing.T, pbx *testPBX, tr livestate.DoorTrigger) {
	t.Helper()
	tr.At = time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pbx.srv.runDoorTrigger(t.Context(), tr)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the door trigger did not finish")
	}
}

// TestDoorWebhookRing fails if a ring_door trigger does not ring the Door
// group's members with the doorbell Alert-Info from the webhook's name, if
// the first answer does not win (the other member is CANCELled), if the
// winner does not hear the chime and then get a BYE, if the call is not
// recorded as kind door with the doorbell tag, or if a member in do-not-
// disturb, an ordinary group or no registration is rung (spec S-48).
func TestDoorWebhookRing(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorHookFeatures())
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	c.setCallee(ringForever())
	sink := newAudioSink(t, b)

	fireDoorTrigger(t, pbx, livestate.DoorTrigger{ID: "t1", Name: "Gate bell", Action: "ring_door", RingGroup: "800", Label: "front"})

	inv := waitReq(t, b.invites, "INVITE to 200")
	if got := alertInfos(inv); len(got) != 1 || got[0] != doorbellAlert {
		t.Errorf("Alert-Info = %q, want %q", got, doorbellAlert)
	}
	if from := inv.From(); from == nil || from.DisplayName != "Gate bell" {
		t.Errorf("From = %v, want the webhook name as the caller name", from)
	}
	waitReq(t, b.acks, "ACK to the winner")
	waitReq(t, c.invites, "INVITE to 300")
	waitReq(t, c.cancels, "CANCEL to the member that did not answer first")
	waitReq(t, b.byes, "BYE to the winner after the chime")
	if n := sink.packets(); n < 30 {
		t.Errorf("the winner got %d audio packets, want the chime (about 50 a second for a second)", n)
	}
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindDoor || cd.Source != "Gate bell" || cd.Destination != "800" || cd.RingTag != "doorbell" ||
		cd.FinalStatus != 200 || cd.AnswerTime.IsZero() {
		t.Errorf("CDR = kind %q source %q destination %q tag %q status %d", cd.Kind, cd.Source, cd.Destination, cd.RingTag, cd.FinalStatus)
	}
	wantTrace(t, cd.Trace, "Doorbell webhook Gate bell (front)", "Ringing Door group 800", "Answered by 200", "Chime played")
}

// TestDoorWebhookRingNobody fails if a trigger with nobody to ring rings
// anything or leaves no CDR, or rings a member in do-not-disturb or a group
// that is not a Door group.
func TestDoorWebhookRingNobody(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorHookFeatures("200"))
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)

	// 200 is in do-not-disturb and 300 is not registered.
	fireDoorTrigger(t, pbx, livestate.DoorTrigger{ID: "t2", Name: "Gate bell", Action: "ring_door", RingGroup: "800"})
	noReq(t, b.invites, 300*time.Millisecond, "an INVITE to a member in do-not-disturb")
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindDoor || cd.FinalStatus != 480 || !cd.AnswerTime.IsZero() {
		t.Errorf("CDR = kind %q status %d, want door 480 unanswered", cd.Kind, cd.FinalStatus)
	}
	wantTrace(t, cd.Trace, "Skipped 200: do not disturb", "No registered phone to ring")

	// An ordinary group is never rung by a door webhook.
	fireDoorTrigger(t, pbx, livestate.DoorTrigger{ID: "t3", Name: "Gate bell", Action: "ring_door", RingGroup: "810"})
	noReq(t, b.invites, 300*time.Millisecond, "an INVITE for a group that is not a Door group")
	cd = pbx.nextCDR(t)
	wantTrace(t, cd.Trace, "Door group 810 is not configured on this node")
}

// TestDoorWebhookChimePage fails if a chime_page trigger does not send the
// paging group's members an auto-answer INVITE from the webhook's name, play
// them the chime and hang up, or if its CDR is not kind door.
func TestDoorWebhookChimePage(t *testing.T) {
	pbx := startPBX(t, doorDevices(), withMedia(false), doorHookFeatures())
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	sinkB, sinkC := newAudioSink(t, b), newAudioSink(t, c)

	fireDoorTrigger(t, pbx, livestate.DoorTrigger{ID: "t4", Name: "Gate bell", Action: "chime_page", PagingCode: "*60"})

	for name, ph := range map[string]*phone{"200": b, "300": c} {
		inv := waitReq(t, ph.invites, "page INVITE to "+name)
		var ci string
		for _, h := range inv.GetHeaders("Call-Info") {
			ci += h.Value()
		}
		if !strings.Contains(ci, "answer-after=0") || len(alertInfos(inv)) != 1 || alertInfos(inv)[0] == doorbellAlert {
			t.Errorf("%s: Call-Info %q Alert-Info %q, want the auto-answer headers and not the doorbell tag", name, ci, alertInfos(inv))
		}
		if pai := inv.GetHeader("P-Asserted-Identity"); pai == nil || !strings.Contains(pai.Value(), "Gate bell") {
			t.Errorf("%s: P-Asserted-Identity = %v, want the webhook name", name, pai)
		}
		waitReq(t, ph.byes, "BYE to "+name)
	}
	if sinkB.packets() < 30 || sinkC.packets() < 30 {
		t.Errorf("chime packets %d and %d, want both members to hear it", sinkB.packets(), sinkC.packets())
	}
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindDoor || cd.Destination != "*60" || cd.FinalStatus != 200 {
		t.Errorf("CDR = kind %q destination %q status %d", cd.Kind, cd.Destination, cd.FinalStatus)
	}
	wantTrace(t, cd.Trace, "Chiming paging group Hall", "Chime played")
}

// fakeDoorQueue is the trigger queue of a test.
type fakeDoorQueue struct {
	mu sync.Mutex
	q  []livestate.DoorTrigger
}

func (f *fakeDoorQueue) TakeDoorTrigger(context.Context) (livestate.DoorTrigger, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.q) == 0 {
		return livestate.DoorTrigger{}, false, nil
	}
	t := f.q[0]
	f.q = f.q[1:]
	return t, true, nil
}

// TestDoorTriggerLoop fails if a trigger queued for the node is not taken
// and run: the loop is what turns the API's queue into a ring.
func TestDoorTriggerLoop(t *testing.T) {
	q := &fakeDoorQueue{q: []livestate.DoorTrigger{{ID: "t5", Name: "Gate bell", Action: "ring_door", RingGroup: "800", At: time.Now()}}}
	pbx := startPBX(t, doorDevices(), withMedia(false), doorHookFeatures(), func(_ *Config, d *Deps) { d.DoorTriggers = q })
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	waitReq(t, b.invites, "the INVITE from the trigger loop")
	waitReq(t, b.byes, "BYE after the chime")
	if cd := pbx.nextCDR(t); cd.Kind != cdr.KindDoor {
		t.Errorf("CDR kind %q, want door", cd.Kind)
	}
}
