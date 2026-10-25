package sip

import (
	"context"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/valkey-io/valkey-go"
)

// memPark is an in-memory ParkState with the semantics of the Valkey one:
// the tests run against it when no Valkey is available, and against Valkey
// itself when HELLO_TEST_VALKEY_ADDR is set (TestCallPark).
type memPark struct {
	mu      sync.Mutex
	slots   map[string]memSlot
	cmds    map[string][]livestate.ParkCommand
	replies map[string]livestate.ParkReply
}

type memSlot struct {
	p   livestate.ParkedCall
	exp time.Time
}

func newMemPark() *memPark {
	return &memPark{slots: map[string]memSlot{}, cmds: map[string][]livestate.ParkCommand{}, replies: map[string]livestate.ParkReply{}}
}

func (m *memPark) live(slot string) (memSlot, bool) {
	s, ok := m.slots[slot]
	if ok && time.Now().After(s.exp) {
		delete(m.slots, slot)
		return memSlot{}, false
	}
	return s, ok
}

func (m *memPark) ParkReserve(_ context.Context, p livestate.ParkedCall, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(p.Slot); ok {
		return false, nil
	}
	m.slots[p.Slot] = memSlot{p, time.Now().Add(ttl)}
	return true, nil
}

func (m *memPark) ParkGet(_ context.Context, slot string) (livestate.ParkedCall, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.live(slot)
	return s.p, ok, nil
}

func (m *memPark) ParkClaim(_ context.Context, slot string) (livestate.ParkedCall, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.live(slot)
	delete(m.slots, slot)
	return s.p, ok, nil
}

func (m *memPark) ParkRefresh(_ context.Context, slot, callID string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.live(slot)
	if !ok || s.p.CallID != callID {
		return false, nil
	}
	s.exp = time.Now().Add(ttl)
	m.slots[slot] = s
	return true, nil
}

func (m *memPark) ParkUpdate(_ context.Context, p livestate.ParkedCall, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.live(p.Slot)
	if !ok || s.p.CallID != p.CallID {
		return false, nil
	}
	m.slots[p.Slot] = memSlot{p, time.Now().Add(ttl)}
	return true, nil
}

func (m *memPark) ParkRelease(_ context.Context, slot, callID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.live(slot); ok && s.p.CallID == callID {
		delete(m.slots, slot)
	}
	return nil
}

func (m *memPark) ParkList(context.Context) ([]livestate.ParkedCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []livestate.ParkedCall{}
	for slot := range m.slots {
		if s, ok := m.live(slot); ok {
			out = append(out, s.p)
		}
	}
	livestate.SortParked(out)
	return out, nil
}

func (m *memPark) ParkSend(_ context.Context, node string, cmd livestate.ParkCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmds[node] = append(m.cmds[node], cmd)
	return nil
}

func (m *memPark) ParkTake(_ context.Context, node string) (livestate.ParkCommand, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.cmds[node]
	if len(q) == 0 {
		return livestate.ParkCommand{}, false, nil
	}
	m.cmds[node] = q[1:]
	return q[0], true, nil
}

func (m *memPark) ParkPutReply(_ context.Context, r livestate.ParkReply) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replies[r.ID] = r
	return nil
}

func (m *memPark) ParkGetReply(_ context.Context, id string) (livestate.ParkReply, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.replies[id]
	return r, ok, nil
}

// parkStore is the park store of a test: Valkey (its own database) when
// HELLO_TEST_VALKEY_ADDR is set, memory otherwise.
func parkStore(t *testing.T) ParkState {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		return newMemPark()
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 7})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return livestate.New(c)
}

// withPark wires the park store and a node name, the park lot, the park and
// pickup feature codes and the pickup group {200, 300, 210}.
func withPark(store ParkState, node string, lot snapshot.ParkLot, groups ...snapshot.PickupGroup) pbxOpt {
	return func(c *Config, d *Deps) {
		c.NodeID = node
		d.Park = store
		withFeatures(func(s *snapshot.Snapshot) {
			s.WithParkLots([]snapshot.ParkLot{lot}).WithPickupGroups(groups)
			s.WithFeatureCodes([]snapshot.FeatureCode{
				{Code: "*74", Action: ActionPark}, {Code: "*75", Action: ActionParkRetrieve},
				{Code: "*40", Action: ActionPickupGroup}, {Code: "*41", Action: ActionPickupDirected},
			})
		})(c, d)
	}
}

var (
	testLot      = snapshot.ParkLot{ID: 1, Name: "default", Start: 701, End: 703, ReturnSecs: 60, VoicemailSecs: 60}
	testFrontGrp = snapshot.PickupGroup{ID: 1, Name: "front", Members: []string{"200", "300", "210"}}
)

// readRTP reads one RTP packet from conn within d.
func readRTP(conn net.PacketConn, d time.Duration) (*media.RTPPacket, bool) {
	buf := make([]byte, 2048)
	_ = conn.SetReadDeadline(time.Now().Add(d))
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, false
		}
		if p, ok := media.ParseRTP(buf[:n]); ok {
			return p, true
		}
	}
}

// hears reports whether conn receives an RTP packet with payload within d,
// skipping the packets (silence, tones) that arrive before it.
func hears(conn net.PacketConn, payload string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p, ok := readRTP(conn, 200*time.Millisecond); ok && string(p.Payload) == payload {
			return true
		}
	}
	return false
}

// countRTP counts the packets conn receives until stop is closed.
func countRTP(conn net.PacketConn, stop <-chan struct{}) *atomic.Int64 {
	var n atomic.Int64
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if k, _, err := conn.ReadFrom(buf); err == nil && k >= 12 {
				n.Add(1)
			}
		}
	}()
	return &n
}

// waitNotifyState reads the phone's NOTIFYs until one carries the dialog
// state.
func waitNotifyState(t *testing.T, p *phone, state string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case r := <-p.notifies:
			if strings.Contains(string(r.Body()), "<state>"+state+"</state>") {
				return
			}
		case <-deadline:
			t.Fatalf("no NOTIFY with state %q", state)
		}
	}
}

// parkedCall is a call a1 (100) made to b1 (200) that b answered, with both
// media feeds latched on the relay.
type parkedCall struct {
	r            callResult
	feedA, feedB *rtpFeed
	dssB         *sipgo.DialogServerSession
}

func establish(t *testing.T, a, b *phone) parkedCall {
	t.Helper()
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	invB := waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b")
	pc := parkedCall{r: r, feedA: startRTPFeed(t, r.dcs.InviteResponse.Body()), feedB: startRTPFeed(t, invB.Body()),
		dssB: b.serverFor(invB)}
	pc.feedA.silence(0.1)
	pc.feedB.silence(0.1)
	return pc
}

// parkBy dials the park code from the caller (a1) by INFO digits.
func parkBy(t *testing.T, a *phone, pc parkedCall, code string) {
	t.Helper()
	seq := pc.r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, pc.r.dcs, code, &seq)
}

// occupied waits for slot to hold a call parked by ext and returns it.
func occupied(t *testing.T, st ParkState, slot, parkedBy string) livestate.ParkedCall {
	t.Helper()
	var got livestate.ParkedCall
	eventually(t, "slot "+slot+" occupied", func() bool {
		p, ok, _ := st.ParkGet(context.Background(), slot)
		got = p
		return ok && p.ParkedBy == parkedBy
	})
	return got
}

func slotFree(t *testing.T, st ParkState, slot string) {
	t.Helper()
	eventually(t, "slot "+slot+" free", func() bool {
		_, ok, _ := st.ParkGet(context.Background(), slot)
		return !ok
	})
}

// TestCallPark: the park lot, end to end. It fails if
//   - *74 (digits in the call) or a REFER to *74 or *74<slot> does not park,
//   - the parker does not hear the slot, or is not released,
//   - the parked party is dropped, or does not stay on the call,
//   - *75<slot> or the slot number does not connect the retriever both ways,
//   - the same slot retrieved from another replica does not connect,
//   - two simultaneous retrieves both connect,
//   - an unretrieved call does not ring its parker again and then go to the
//     parker's voicemail,
//   - hanging up on either side does not end the other,
//   - a slot's dialog BLF does not show occupancy.
func TestCallPark(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot))
	a, b, c, d := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc"), newPhone(t, pbx, "d1", "pd")
	for _, p := range []*phone{a, b, c, d} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}

	// A BLF key on slot 701 shows it free.
	if res := c.subscribe(t, "701", 300); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE to slot 701 = %d, want 200", res.StatusCode)
	}
	waitNotifyState(t, c, "terminated")

	// 1. The caller parks by dialling *74: system chooses slot 701.
	pc := establish(t, a, b)
	stop := make(chan struct{})
	heard := countRTP(pc.feedA.conn, stop)
	parkBy(t, a, pc, "*74")
	rec := occupied(t, st, "701", "100")
	if rec.Caller != "200" || rec.Node != "sip-a" || rec.Lot != "default" {
		t.Fatalf("parked record = %+v", rec)
	}
	waitReq(t, a.byes, "BYE to the parker")
	close(stop)
	if n := heard.Load(); n < 10 {
		t.Fatalf("the parker heard %d RTP packets, want the slot announcement (>= 10)", n)
	}
	noReq(t, b.byes, 300*time.Millisecond, "BYE to the parked party")
	waitNotifyState(t, c, "confirmed") // the slot's BLF shows it occupied

	// 2. c retrieves with the retrieve code and the slot: both ways audio.
	r2 := waitCall(t, dial(t.Context(), c, "*75701"))
	if r2.err != nil {
		t.Fatalf("retrieve: %v", r2.err)
	}
	anchoredSDP(t, pbx, r2.dcs.InviteResponse.Body())
	slotFree(t, st, "701")
	waitNotifyState(t, c, "terminated")
	feedC := startRTPFeed(t, r2.dcs.InviteResponse.Body())
	feedC.packet(0, make([]byte, 160), false)
	time.Sleep(100 * time.Millisecond)
	pc.feedB.packet(0, []byte("from-b-to-the-retriever....."), false)
	if !hears(feedC.conn, "from-b-to-the-retriever.....", 3*time.Second) {
		t.Fatalf("the retriever did not hear the parked party")
	}
	feedC.packet(0, []byte("from-c-to-the-parked-party.."), false)
	if !hears(pc.feedB.conn, "from-c-to-the-parked-party..", 3*time.Second) {
		t.Fatalf("the parked party did not hear the retriever")
	}
	// The retriever hangs up: the parked party's call ends.
	hangup(t, r2.dcs)
	waitReq(t, b.byes, "BYE to the parked party when the retriever hangs up")
	var traces []string
	for range 2 {
		cd := pbx.nextCDR(t)
		for _, s := range cd.Trace {
			traces = append(traces, s.Text)
		}
	}
	joined := strings.Join(traces, "\n")
	for _, want := range []string{"Parked in slot 701", "Retrieved from slot 701 by 300", "Retrieved the call parked in slot 701"} {
		if !strings.Contains(joined, want) {
			t.Errorf("CDR traces lack %q:\n%s", want, joined)
		}
	}

	// 3. Parking again, two retrievers race for the slot by its number.
	pc = establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	rc, rd := dial(t.Context(), c, "701"), dial(t.Context(), d, "701")
	res := []callResult{waitCall(t, rc), waitCall(t, rd)}
	won := 0
	for i, r := range res {
		if r.err == nil {
			won++
			hangup(t, r.dcs)
			_ = i
		}
	}
	if won != 1 {
		t.Fatalf("%d of two simultaneous retrieves connected, want exactly 1", won)
	}
	waitReq(t, b.byes, "BYE to the parked party")

	// 4. Park to a chosen slot by *74<slot>, then retrieve it by *75<slot>.
	pc = establish(t, a, b)
	parkBy(t, a, pc, "*74702")
	occupied(t, st, "702", "100")
	if _, ok, _ := st.ParkGet(context.Background(), "701"); ok {
		t.Fatal("*74702 parked in the wrong slot")
	}
	waitReq(t, a.byes, "BYE to the parker")
	r4 := waitCall(t, dial(t.Context(), c, "*75702"))
	if r4.err != nil {
		t.Fatalf("retrieve 702: %v", r4.err)
	}
	// The parked party hangs up: the retriever's call ends.
	byeFrom(t, pc.dssB)
	waitReq(t, c.byes, "BYE to the retriever when the parked party hangs up")
	pbx.nextCDR(t)
	pbx.nextCDR(t)
}

// byeFrom hangs up a call from the phone that was called.
func byeFrom(t *testing.T, dss *sipgo.DialogServerSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := dss.Bye(ctx); err != nil {
		t.Fatalf("BYE from the callee: %v", err)
	}
}

// serverFor is the phone's server dialog for the INVITE it received.
func (p *phone) serverFor(inv *sip.Request) *sipgo.DialogServerSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.servers[inv.CallID().Value()]
}

// TestCallParkRefer: the Park softkey is a blind REFER to the park code. It
// fails if a REFER from the called phone to *74 or *74<slot> does not park
// the other party, the REFER's subscription does not report success, the
// referring phone is not released, or a REFER to an occupied slot is not
// refused.
func TestCallParkRefer(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}

	// b (called) parks a by a REFER to *74.
	pc := establish(t, a, b)
	if res := b.sendReferFromCallee(t, pc.dssB, "<sip:*74@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	final := waitReq(t, b.notifies, "the REFER's NOTIFY")
	if !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("NOTIFY = %q, want 200 OK", final.Body())
	}
	rec := occupied(t, st, "701", "200")
	if rec.Caller != "100" {
		t.Fatalf("parked record = %+v", rec)
	}
	waitReq(t, b.byes, "BYE to the referring phone")
	noReq(t, a.byes, 300*time.Millisecond, "BYE to the parked party")

	// c retrieves it; a is connected to c.
	r := waitCall(t, dial(t.Context(), c, "*75701"))
	if r.err != nil {
		t.Fatalf("retrieve: %v", r.err)
	}
	hangup(t, pc.r.dcs) // the parked party hangs up: the retriever hears BYE
	waitReq(t, c.byes, "BYE to the retriever")
	pbx.nextCDR(t)
	pbx.nextCDR(t)

	// A REFER to *74<slot> parks in that slot; an occupied slot is refused.
	pc = establish(t, a, b)
	if res := b.sendReferFromCallee(t, pc.dssB, "<sip:*74703@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	waitReq(t, b.notifies, "the REFER's NOTIFY")
	occupied(t, st, "703", "200")
	waitReq(t, b.byes, "BYE to the referring phone")
	pc2 := establish(t, a, b)
	if res := b.sendReferFromCallee(t, pc2.dssB, "<sip:*74703@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	if n := waitReq(t, b.notifies, "the refused REFER's NOTIFY"); !strings.Contains(string(n.Body()), "486") {
		t.Fatalf("NOTIFY = %q, want a refusal", n.Body())
	}
	if _, ok, _ := st.ParkGet(context.Background(), "703"); !ok {
		t.Fatal("the occupied slot lost its call")
	}
	noReq(t, b.byes, 300*time.Millisecond, "BYE: a refused park leaves the call up")
}

// TestCallParkReturn: an unretrieved call rings its parker again, and goes to
// the parker's voicemail when nobody answers. It fails if the parker is not
// rung after the return time, the parked party is not bridged to the parker
// who answers, the slot stays occupied after the return, or an unanswered
// return does not move the parked party onto the voicemail application.
func TestCallParkReturn(t *testing.T) {
	st := parkStore(t)
	lot := snapshot.ParkLot{ID: 1, Name: "default", Start: 701, End: 703, ReturnSecs: 1, VoicemailSecs: 1}
	vm := newVMStore()
	vm.boxes["100"] = VoicemailBox{ID: 1, Extension: "100"}
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", lot), withVoicemail(vm, &fakeObjects{}),
		withFeatures(func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "100", Name: "Ext 100", VoicemailEnabled: true, VoicemailBoxID: 1}})
		}))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))

	// The parker answers the returned call.
	a.setCallee(answerAfter(nil))
	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	back := waitReq(t, a.invites, "the returned call")
	if back.From().Address.User != "200" {
		t.Fatalf("returned call From = %s, want the parked party 200", back.From().Address.User)
	}
	waitReq(t, a.acks, "ACK of the returned call")
	slotFree(t, st, "701")
	// The parker hangs up the returned call: the parked party is released.
	byeFrom(t, a.serverFor(back))
	waitReq(t, b.byes, "BYE to the parked party")

	// Nobody answers: the parked party moves onto the parker's voicemail.
	a.setCallee(ringForever())
	pc = establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	waitReq(t, a.invites, "the returned call rings the parker")
	re := waitReq(t, b.reinvites, "the parked party's re-INVITE onto voicemail")
	if sdp, err := media.ParseAudioSDP(re.Body()); err != nil || sdp.Port == 0 {
		t.Fatalf("voicemail re-INVITE SDP = %q, %v", re.Body(), err)
	}
	waitReq(t, a.cancels, "CANCEL of the returned call")
	byeFrom(t, pc.dssB)
}

// TestCallParkReplicas: a call parked on one node is retrieved on another,
// and either hang-up ends the other side. It fails if the second node
// cannot connect the retriever to the first node's relay, the BLF on its
// phones does not follow a park made elsewhere, or a hang-up on one node
// does not reach the other.
func TestCallParkReplicas(t *testing.T) {
	st := parkStore(t)
	pbx1 := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot))
	pbx2 := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-b", testLot))
	a, b := newPhone(t, pbx1, "a1", "pa"), newPhone(t, pbx1, "b1", "pb1")
	c := newPhone(t, pbx2, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}
	if res := c.subscribe(t, "701", 300); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE on the second node = %d", res.StatusCode)
	}
	waitNotifyState(t, c, "terminated")

	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitNotifyState(t, c, "confirmed") // a park on node 1 reaches node 2's subscriber

	r := waitCall(t, dial(t.Context(), c, "*75701"))
	if r.err != nil {
		t.Fatalf("retrieve on the second node: %v", r.err)
	}
	feedC := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feedC.packet(0, make([]byte, 160), false)
	time.Sleep(100 * time.Millisecond)
	pc.feedB.packet(0, []byte("across-the-cluster.........."), false)
	if !hears(feedC.conn, "across-the-cluster..........", 3*time.Second) {
		t.Fatalf("the retriever on the second node did not hear the parked party")
	}
	waitNotifyState(t, c, "terminated")

	// The retriever (node 2) hangs up: the parked party (node 1) is ended.
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE to the parked party across nodes")

	// And the other way: the parked party hangs up, the retriever hears BYE.
	pc = establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	r = waitCall(t, dial(t.Context(), c, "701"))
	if r.err != nil {
		t.Fatalf("second retrieve: %v", r.err)
	}
	byeFrom(t, pc.dssB)
	waitReq(t, c.byes, "BYE to the retriever across nodes")
}

// TestCallParkTakeover: a call waiting in a park slot survives its node
// (incall-ha). It fails if the parked call is not replicated as a park
// state, a surviving node does not re-INVITE the parked party onto its own
// relay and put the slot under its own name, a retrieve on the survivor does
// not connect, or the parked party hanging up on the survivor does not end
// the retriever's call.
func TestCallParkTakeover(t *testing.T) {
	st := parkStore(t)
	ha := newFakeHA()
	mem := &fakeMembership{}
	owner := startPBX(t, threeDevices(), withMedia(false), withNodeID("sip-1"), withHA(ha, mem), withPark(st, "sip-1", testLot))
	taker := startPBX(t, threeDevices(), withMedia(false), withNodeID("sip-2"), withHA(ha, mem), withPark(st, "sip-2", testLot))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	c := newPhone(t, taker, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}
	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	rec := waitRecord(t, ha, "the parked call is replicated", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhasePark
	})
	if rec.Legs[0].LocalTag == "" || !strings.Contains(rec.StateDetail, `"slot":"701"`) {
		t.Fatalf("replicated park state = %+v", rec)
	}

	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	re := waitReq(t, b.reinvites, "takeover re-INVITE to the parked party")
	anchoredSDP(t, taker, re.Body())
	waitReq(t, b.acks, "ACK of the parked party's 200")
	eventually(t, "the slot is the survivor's", func() bool {
		p, ok, _ := st.ParkGet(context.Background(), "701")
		return ok && p.Node == "sip-2"
	})

	// c retrieves the call on the survivor and talks to the parked party.
	r := waitCall(t, dial(t.Context(), c, "*75701"))
	if r.err != nil {
		t.Fatalf("retrieve on the survivor: %v", r.err)
	}
	feedC := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feedB := startRTPFeed(t, re.Body())
	feedC.packet(0, make([]byte, 160), false)
	feedB.packet(0, make([]byte, 160), false)
	time.Sleep(100 * time.Millisecond)
	feedB.packet(0, []byte("after-the-takeover.........."), false)
	if !hears(feedC.conn, "after-the-takeover..........", 3*time.Second) {
		t.Fatal("the retriever did not hear the parked party after the takeover")
	}
	// The parked party hangs up on the survivor: the retriever hears BYE.
	hangupHomed(t, b, taker, re, rec.Legs[0])
	waitReq(t, c.byes, "BYE to the retriever")
}

// TestCallParkHold: a hold on a retrieved call reaches the other party, as
// on a bridged call. It fails if the retriever's sendonly re-INVITE is not
// answered recvonly, if the parked party is not re-INVITEd sendonly (and
// sendrecv on resume), or if the parked party's hold does not reach the
// retriever, on one node and across two.
func TestCallParkHold(t *testing.T) {
	for _, across := range []bool{false, true} {
		name := "one node"
		if across {
			name = "across nodes"
		}
		t.Run(name, func(t *testing.T) {
			st := parkStore(t)
			pbx1 := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot))
			pbx2 := pbx1
			if across {
				pbx2 = startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-b", testLot))
			}
			a, b := newPhone(t, pbx1, "a1", "pa"), newPhone(t, pbx1, "b1", "pb1")
			c := newPhone(t, pbx2, "c1", "pc")
			for _, p := range []*phone{a, b, c} {
				p.register(t)
				p.setCallee(answerAfter(nil))
			}
			pc := establish(t, a, b)
			parkBy(t, a, pc, "*74")
			occupied(t, st, "701", "100")
			r := waitCall(t, dial(t.Context(), c, "*75701"))
			if r.err != nil {
				t.Fatalf("retrieve: %v", r.err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			// The retriever holds, then resumes.
			for _, step := range []struct{ offer, answer, peer string }{
				{"sendonly", "recvonly", "sendonly"},
				{"sendrecv", "sendrecv", "sendrecv"},
			} {
				re := sip.NewRequest(sip.INVITE, r.dcs.InviteResponse.Contact().Address)
				re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
				re.SetBody([]byte(c.sdp + "a=" + step.offer + "\r\n"))
				res, err := r.dcs.Do(ctx, re)
				if err != nil || res.StatusCode != 200 {
					t.Fatalf("%s re-INVITE = %v, %v", step.offer, res, err)
				}
				if got := media.SDPDirection(res.Body()); got != step.answer {
					t.Fatalf("%s answered %q, want %s", step.offer, got, step.answer)
				}
				if err := r.dcs.WriteRequest(sip.NewRequest(sip.ACK, r.dcs.InviteResponse.Contact().Address)); err != nil {
					t.Fatal(err)
				}
				got := waitReq(t, b.reinvites, step.offer+" re-INVITE to the parked party")
				if dir := media.SDPDirection(got.Body()); dir != step.peer {
					t.Fatalf("parked party re-INVITEd %q, want %s: %q", dir, step.peer, got.Body())
				}
			}

			// The parked party holds: the retriever is re-INVITEd sendonly.
			re := sip.NewRequest(sip.INVITE, pc.dssB.InviteRequest.Contact().Address)
			re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			re.SetBody([]byte(b.sdp + "a=sendonly\r\n"))
			res, err := pc.dssB.Do(ctx, re)
			if err != nil || res.StatusCode != 200 {
				t.Fatalf("parked party's hold = %v, %v", res, err)
			}
			if got := media.SDPDirection(res.Body()); got != "recvonly" {
				t.Fatalf("parked party's hold answered %q, want recvonly", got)
			}
			got := waitReq(t, c.reinvites, "hold re-INVITE to the retriever")
			if dir := media.SDPDirection(got.Body()); dir != "sendonly" {
				t.Fatalf("retriever re-INVITEd %q, want sendonly", dir)
			}
			hangup(t, r.dcs)
			waitReq(t, b.byes, "BYE to the parked party")
		})
	}
}

// TestCallParkSpokenSlot: with the park clips generated, the parker hears
// the slot spoken (the lead-in, then each digit) instead of tones. It fails
// if the clips are not fetched, if the parker hears the short tone sequence
// instead of the 2 s lead-in plus digits, or if the call trace does not say
// the slot was announced by voice.
func TestCallParkSpokenSlot(t *testing.T) {
	objs := &fakeObjects{}
	anns := map[string]string{parkSlotPrompt: "ann/park.slot.wav"}
	_ = objs.Put(context.Background(), "ann/park.slot.wav", media.EncodeWAV(make([]byte, 2*8000*2), 8000)) // 2 s
	for _, d := range []byte("0123456789") {
		obj := "ann/" + parkDigitPrompt(d) + ".wav"
		anns[parkDigitPrompt(d)] = obj
		_ = objs.Put(context.Background(), obj, media.EncodeWAV(make([]byte, 2*8000/5), 8000)) // 200 ms
	}
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot),
		withVoicemail(newVMStore(), objs), withFeatures(func(s *snapshot.Snapshot) { s.WithAnnouncements(anns) }))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	for _, p := range []*phone{a, b} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}
	pc := establish(t, a, b)
	stop := make(chan struct{})
	heard := countRTP(pc.feedA.conn, stop)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")
	close(stop)
	// Tones for 701 last under a second (< 50 packets); the spoken
	// announcement is over 3 s.
	if n := heard.Load(); n < 120 {
		t.Fatalf("the parker heard %d RTP packets, want the spoken slot (>= 120)", n)
	}
	gets := strings.Join(objs.snapshotGets(), " ")
	for _, want := range []string{"ann/park.slot.wav", "ann/park.digit.7.wav", "ann/park.digit.0.wav", "ann/park.digit.1.wav"} {
		if !strings.Contains(gets, want) {
			t.Fatalf("clip %s not fetched: %s", want, gets)
		}
	}
	byeFrom(t, pc.dssB)
	cd := pbx.nextCDR(t)
	var traces []string
	for _, s := range cd.Trace {
		traces = append(traces, s.Text)
	}
	if joined := strings.Join(traces, "\n"); !strings.Contains(joined, "Slot 701 announced by voice") {
		t.Fatalf("CDR trace lacks the spoken announcement:\n%s", joined)
	}
}

// TestCallParkMusic: a parked party hears the music on hold class (the
// default class here), and the music stops once the call is retrieved. It
// fails if the parked party hears silence or the old periodic tone, if the
// class track is not fetched, or if the music plays over the retriever.
func TestCallParkMusic(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/hold1.wav", loudWAV())
	classes := []snapshot.MOHClass{{ID: 1, Name: "default", IsDefault: true, Names: []string{"hold1"}, Objects: []string{"ann/hold1.wav"}}}
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot),
		withVoicemail(newVMStore(), objs), withFeatures(func(s *snapshot.Snapshot) { s.WithMOHClasses(classes) }))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}
	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74")
	occupied(t, st, "701", "100")
	waitReq(t, a.byes, "BYE to the parker")

	n, peak := rtpCount(pc.feedB.conn, 1500*time.Millisecond)
	if n < 40 || peak < 500 {
		t.Fatalf("the parked party got %d packets (peak %d) in 1.5s, want a steady music stream", n, peak)
	}
	if !slices.Contains(objs.snapshotGets(), "ann/hold1.wav") {
		t.Fatalf("the class track was never fetched: %v", objs.snapshotGets())
	}

	r2 := waitCall(t, dial(t.Context(), c, "*75701"))
	if r2.err != nil {
		t.Fatalf("retrieve: %v", r2.err)
	}
	rtpCount(pc.feedB.conn, 200*time.Millisecond) // drain what was in flight
	if n, peak := rtpCount(pc.feedB.conn, 600*time.Millisecond); peak >= 500 {
		t.Fatalf("music continued after the retrieve: %d packets, peak %d", n, peak)
	}
	hangup(t, r2.dcs)
	waitReq(t, b.byes, "BYE to the parked party")
}
