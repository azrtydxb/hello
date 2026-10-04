// Phase 4 feature tests: hold, transfers, forwarding/DND, groups, voicemail,
// presence and feature codes (spec S-1..S-11, S-13). The doubles here stand
// in for the store-side implementations that the control plane wires.
package sip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"github.com/azrtydxb/hello/internal/snapshot"
)

// shortRing replaces the default 5s ring timeout, so exhaustion paths run in
// milliseconds.
func shortRing(d time.Duration) pbxOpt {
	return func(c *Config, _ *Deps) { c.RingTimeout = d }
}

// fakeSettings records queued extension updates.
type fakeSettings struct {
	mu      sync.Mutex
	updates []ExtUpdate
	down    bool
}

func (f *fakeSettings) Apply(u ExtUpdate) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return false
	}
	f.updates = append(f.updates, u)
	return true
}

func (f *fakeSettings) got() []ExtUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExtUpdate(nil), f.updates...)
}

// fakeVMStore is the voicemail metadata store.
type fakeVMStore struct {
	mu      sync.Mutex
	boxes   map[string]VoicemailBox
	pwOK    map[int64]string // box id -> the only accepted password
	nextID  int64
	msgs    []VoicemailMessage
	counts  [][2]int // per insert: unread, total after
	failIns bool
}

func newVMStore() *fakeVMStore {
	return &fakeVMStore{boxes: map[string]VoicemailBox{}, pwOK: map[int64]string{}, nextID: 100}
}

func (f *fakeVMStore) Box(_ context.Context, ext string) (VoicemailBox, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[ext]
	return b, ok, nil
}

func (f *fakeVMStore) PasswordOK(_ context.Context, boxID int64, password string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want, ok := f.pwOK[boxID]
	return ok && password == want, nil
}

func (f *fakeVMStore) InsertMessage(_ context.Context, boxID int64, object, caller string, durationMs int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failIns {
		return 0, errDown
	}
	f.nextID++
	m := VoicemailMessage{ID: f.nextID, BoxID: boxID, MinioObject: object, Caller: caller, DurationMs: durationMs, CreatedAt: time.Now()}
	f.msgs = append(f.msgs, m)
	unread := 0
	for _, m := range f.msgs {
		if !m.Heard {
			unread++
		}
	}
	f.counts = append(f.counts, [2]int{unread, len(f.msgs)})
	return m.ID, nil
}

func (f *fakeVMStore) MessageCounts(_ context.Context, boxID int64) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	unread, total := 0, 0
	for _, m := range f.msgs {
		if m.BoxID == boxID {
			total++
			if !m.Heard {
				unread++
			}
		}
	}
	return unread, total, nil
}

func (f *fakeVMStore) ListMessages(_ context.Context, boxID int64) ([]VoicemailMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []VoicemailMessage
	for _, m := range f.msgs {
		if m.BoxID == boxID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeVMStore) MarkHeard(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.msgs {
		if f.msgs[i].ID == id {
			f.msgs[i].Heard = true
		}
	}
	return nil
}

// fakeObjects is the audio store. failPuts makes the first N puts fail, so
// the retry path is exercised.
type fakeObjects struct {
	mu       sync.Mutex
	objs     map[string][]byte
	down     bool
	failPuts int
}

func (f *fakeObjects) Put(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errDown
	}
	if f.failPuts > 0 {
		f.failPuts--
		return errDown
	}
	if f.objs == nil {
		f.objs = map[string][]byte{}
	}
	f.objs[key] = data
	return nil
}

func (f *fakeObjects) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.objs[key]
	if !ok {
		return nil, fmt.Errorf("no such object %s", key)
	}
	return d, nil
}

// withFeatures mutates the test snapshot: extension rows, ring groups and
// feature codes.
func withFeatures(fn func(s *snapshot.Snapshot)) pbxOpt {
	return func(_ *Config, d *Deps) { fn(d.Snapshots.(*fakeSnaps).p.Load()) }
}

// withVoicemail wires the voicemail store, audio store and media anchor.
func withVoicemail(vm VoicemailStore, objs ObjectStore) pbxOpt {
	return func(c *Config, d *Deps) {
		d.Voicemails, d.Objects = vm, objs
		c.PresenceTTL = 5 * time.Second
	}
}

// anchored turns the media anchor on for the PBX (after startPBX).
func anchored(p *testPBX) {
	p.srv.SetMediaAnchor(media.NewAnchor("127.0.0.1", discard))
}

// threeDevices is callerDevices plus a third extension (300) for transfer
// targets and a fourth (210) for the no-answer forwarding case.
func threeDevices() []snapshot.Device {
	return append(callerDevices(), dev(3, "c1", "300", "pc"), dev(4, "d1", "210", "pd"))
}

// TestHold fails if a held call's re-INVITE is not relayed with its sendonly
// body intact, if hello_hold_active does not count the held call and clear
// on resume, or if holding changes the call's CDR.
func TestHold(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	// a holds: sendonly offer.
	sdp := "v=0\r\no=a 2 2 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 1 RTP/AVP 0\r\na=sendonly\r\n"
	if res := a.reinviteAsCaller(t, r.dcs, sdp); res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %d", res.StatusCode)
	}
	held := waitReq(t, b.reinvites, "hold re-INVITE relayed to b")
	if !sdpHeld(held.Body()) {
		t.Fatalf("hold relayed without the hold signal: %s", held.Body())
	}
	if v := pbx.metric(t, "hello_hold_active", nil); v != 1 {
		t.Fatalf("hello_hold_active = %v during hold, want 1", v)
	}
	// No CDR while held.
	pbx.noCDR(t, 200*time.Millisecond)
	// a resumes: sendrecv.
	resume := "v=0\r\no=a 3 3 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 1 RTP/AVP 0\r\na=sendrecv\r\n"
	if res := a.reinviteAsCaller(t, r.dcs, resume); res.StatusCode != 200 {
		t.Fatalf("resume re-INVITE = %d", res.StatusCode)
	}
	waitReq(t, b.reinvites, "resume re-INVITE relayed to b")
	if v := pbx.metric(t, "hello_hold_active", nil); v != 0 {
		t.Fatalf("hello_hold_active = %v after resume, want 0", v)
	}
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	if cd.TerminationSide != cdr.SideCaller || cd.AnswerTime.IsZero() {
		t.Fatalf("CDR changed by hold: %+v", cd)
	}
}

// sendReferFromCallee sends a REFER from a phone that was called (the UAS of
// its dialog with the PBX): the transferee's usual side in a blind transfer.
func (p *phone) sendReferFromCallee(t *testing.T, dss *sipgo.DialogServerSession, referTo string) *sip.Response {
	t.Helper()
	inv := dss.InviteRequest
	req := sip.NewRequest(sip.REFER, *dss.InviteResponse.Contact().Address.Clone())
	req.AppendHeader(&sip.FromHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagOfTest(inv.To())}}})
	req.AppendHeader(&sip.ToHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagOfTest(inv.From())}}})
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.REFER})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Refer-To", referTo))
	req.SetTransport("UDP")
	req.SetDestination(p.pbx)
	return p.do(req)
}

// TestBlindTransfer fails if the transferee is not connected to the target
// after REFER with NOTIFYs in between, if the original caller is not kept as
// the remote party, or if the transfer metric does not move.
func TestBlindTransfer(t *testing.T) {
	pbx := startPBX(t, threeDevices())
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	a.register(t)
	b.register(t)
	c.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b") // the 200 is out before the REFER
	dss := <-serversOf(b)
	c.setCallee(answerAfter(nil))
	// b (the transferee) refers a to 300.
	if res := b.sendReferFromCallee(t, dss, "<sip:300@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	// b learns progress, then success.
	notified := waitReq(t, b.notifies, "NOTIFY to the transferee")
	if !strings.Contains(string(notified.Body()), "100 Trying") {
		t.Fatalf("first NOTIFY = %q", notified.Body())
	}
	final := waitReq(t, b.notifies, "final NOTIFY to the transferee")
	if !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("final NOTIFY = %q", final.Body())
	}
	// The transferee's leg is dropped only now.
	waitReq(t, b.byes, "BYE to the transferee")
	// c is rung with the original caller kept, and answers; a is connected.
	inv := waitReq(t, c.invites, "INVITE to the target")
	if inv.From().Address.User != "100" {
		t.Fatalf("target INVITE From = %s, want the original caller 100", inv.From().Address.User)
	}
	waitReq(t, c.acks, "ACK to the target")
	waitReq(t, a.reinvites, "re-INVITE to the caller with the target's answer")
	if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "blind", "result": "answered"}); v != 1 {
		t.Fatalf("transfers{blind,answered} = %v", v)
	}
	// CDRs: the original call closed as transferred, the new call answered.
	cd := pbx.nextCDR(t)
	if cd.Destination != "200" || cd.FailureReason != "blind transfer" {
		t.Fatalf("original CDR = %+v", cd)
	}
	hangup(t, r.dcs)
	cd2 := pbx.nextCDR(t)
	if cd2.Destination != "300" || cd2.FinalStatus != 200 {
		t.Fatalf("transferred CDR = %+v", cd2)
	}
}

// serversOf exposes the phone's established server sessions.
func serversOf(p *phone) <-chan *sipgo.DialogServerSession {
	ch := make(chan *sipgo.DialogServerSession, 4)
	p.mu.Lock()
	for _, d := range p.servers {
		select {
		case ch <- d:
		default:
		}
	}
	p.mu.Unlock()
	close(ch)
	return ch
}

// TestBlindTransferNotifyFailure fails if an unroutable target does not get
// a failure NOTIFY, or if the failed transfer does not leave both legs up
// and talking (spec edge case).
func TestBlindTransferNotifyFailure(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	waitReq(t, b.acks, "ACK to b") // the 200 is out before the REFER
	dss := <-serversOf(b)
	// An external number with no route configured.
	if res := b.sendReferFromCallee(t, dss, "<sip:00999123456@"+testDomain+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	waitReq(t, b.notifies, "progress NOTIFY")
	final := waitReq(t, b.notifies, "failure NOTIFY")
	if !strings.Contains(string(final.Body()), "404") {
		t.Fatalf("failure NOTIFY = %q, want a 404 fragment", final.Body())
	}
	if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "blind", "result": "failed"}); v < 1 {
		t.Fatalf("transfers{blind,failed} = %v", v)
	}
	// Both legs stay up: the call still relays re-INVITEs.
	if res := a.reinviteAsCaller(t, r.dcs, "v=0\r\no=a 9 9 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 1 RTP/AVP 0\r\n"); res.StatusCode != 200 {
		t.Fatalf("post-failure re-INVITE = %d; the call did not stay up", res.StatusCode)
	}
	waitReq(t, b.reinvites, "relayed re-INVITE to b")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// forwardingSnapshot installs extension feature rows into the test snapshot.
func forwardingSnapshot(rows ...snapshot.Extension) func(*snapshot.Snapshot) {
	return func(s *snapshot.Snapshot) { s.WithExtensionData(rows) }
}

// TestForwarding fails if always/busy/no-answer forwarding does not fire
// when it should (and does not when it should not), and if a forwarding
// chain that returns to a visited extension is not rejected with 408.
func TestForwarding(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(400*time.Millisecond),
		withFeatures(forwardingSnapshot(
			snapshot.Extension{Number: "220", ForwardAlways: "300"},
			snapshot.Extension{Number: "200", ForwardBusy: "300"},
			snapshot.Extension{Number: "210", ForwardNoAnswer: "300"},
			snapshot.Extension{Number: "270", ForwardAlways: "280"},
			snapshot.Extension{Number: "280", ForwardAlways: "270"},
		)))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	d := newPhone(t, pbx, "d1", "pd")
	d.register(t)

	// always: 220 (which has no device at all) never rings anything else, 300 is.
	r := waitCall(t, dial(t.Context(), a, "220"))
	if r.err != nil {
		t.Fatalf("always forward: %v", r.err)
	}
	waitReq(t, c.invites, "always-forward INVITE to 300")
	noReq(t, d.invites, 200*time.Millisecond, "INVITE to the always-forwarded extension")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "always"}); v != 1 {
		t.Fatalf("forwarded{always} = %v", v)
	}
	// busy: 200 answers 486, then 300 is rung.
	b.setCallee(busy())
	r = waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("busy forward: %v", r.err)
	}
	waitReq(t, c.invites, "busy-forward INVITE to 300")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	// no-answer: 210 rings forever past the ring timeout, then 300.
	d.setCallee(ringForever())
	c.setCallee(answerAfter(nil))
	r = waitCall(t, dial(t.Context(), a, "210"))
	if r.err != nil {
		t.Fatalf("no-answer forward: %v", r.err)
	}
	waitReq(t, c.invites, "no-answer-forward INVITE to 300")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "no_answer"}); v != 1 {
		t.Fatalf("forwarded{no_answer} = %v", v)
	}
	// loop: 270 -> 280 -> 270 rejects with 408.
	r = waitCall(t, dial(t.Context(), a, "270"))
	var code int
	if r.err != nil {
		code = responseCode(r.err)
	}
	if code != 408 {
		t.Fatalf("forward loop = %v, want 408", r.err)
	}
	pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "loop"}); v != 1 {
		t.Fatalf("forwarded{loop} = %v", v)
	}
}

// TestDND fails if a DND extension rings instead of being declined, and if
// the feature-code toggles do not follow the state.
func TestDND(t *testing.T) {
	settings := &fakeSettings{}
	pbx := startPBX(t, callerDevices(), withFeatures(forwardingSnapshot(
		snapshot.Extension{Number: "200", DND: true},
	)), func(_ *Config, d *Deps) { d.Settings = settings })
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if code := responseCode(r.err); code != 603 {
		t.Fatalf("call to DND extension = %v, want 603", r.err)
	}
	pbx.nextCDR(t)
	noReq(t, b.invites, 200*time.Millisecond, "INVITE that DND must refuse")
}

// TestRingGroupStrategies runs a table per strategy over the pure ordering
// functions: DND handling, round-robin rotation and wrap, longest-idle
// ordering, weighted distribution by weight, and sequential position order.
func TestRingGroupStrategies(t *testing.T) {
	base := []groupMember{
		{ext: "100", weight: 1, delay: 0},
		{ext: "200", weight: 1, delay: 2},
		{ext: "300", weight: 3, delay: 0},
	}
	exts := func(ms []groupMember) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ext)
		}
		return out
	}
	// ring-all: position order; DND members skipped unless ignored.
	dnd := append(append([]groupMember(nil), base...), groupMember{ext: "400", dnd: true})
	if got := exts(orderGroup("ring-all", dnd, false, nil, 1)); fmt.Sprint(got) != "[100 200 300]" {
		t.Fatalf("ring-all skips DND: %v", got)
	}
	if got := exts(orderGroup("ring-all", dnd, true, nil, 1)); fmt.Sprint(got) != "[100 200 300 400]" {
		t.Fatalf("ring-all ignore_dnd keeps everyone: %v", got)
	}
	// sequential: position order too (the delays space the starts).
	if got := exts(orderGroup("sequential", base, false, nil, 1)); fmt.Sprint(got) != "[100 200 300]" {
		t.Fatalf("sequential order: %v", got)
	}
	// round-robin: the start rotates and wraps.
	rot := new(atomic.Uint64)
	rot.Store(3)
	ms := []groupMember{{ext: "1"}, {ext: "2"}, {ext: "3"}, {ext: "4"}}
	if got := exts(orderRoundRobin(ms, rot)); fmt.Sprint(got) != "[4 1 2 3]" {
		t.Fatalf("round-robin start: %v", got)
	}
	if rot.Load() != 4 {
		t.Fatalf("rotation did not advance: %d", rot.Load())
	}
	if got := exts(orderRoundRobin(ms, rot)); fmt.Sprint(got) != "[1 2 3 4]" {
		t.Fatalf("round-robin wrap: %v", got)
	}
	// longest-idle: never-called first, then by oldest end.
	idle := []groupMember{
		{ext: "late", lastEnd: time.Now().Add(-time.Second)},
		{ext: "never"},
		{ext: "older", lastEnd: time.Now().Add(-time.Hour)},
	}
	if got := exts(orderLongestIdle(idle)); fmt.Sprint(got) != "[never older late]" {
		t.Fatalf("longest-idle order: %v", got)
	}
	// weighted: the weight-3 member leads far more often than the weight-1
	// ones, and the same seed always orders the same way.
	leads := map[string]int{}
	for seed := uint64(0); seed < 200; seed++ {
		got := orderWeighted(base, seed)
		leads[got[0].ext]++
	}
	if leads["300"] < 100 || leads["100"] == 0 || leads["200"] == 0 {
		t.Fatalf("weighted distribution by weight: %v", leads)
	}
	first := exts(orderWeighted(base, 42))
	second := exts(orderWeighted(base, 42))
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatal("weighted order is not deterministic per call")
	}
	// A group whose every member is DND and not ignored has no members.
	if got := orderGroup("ring-all", []groupMember{{ext: "9", dnd: true}}, false, nil, 1); len(got) != 0 {
		t.Fatalf("all-DND group not emptied: %v", got)
	}
}

// TestRingGroupsIntegration fails if a ring-all group does not fork to its
// living members (skipping the caller's own), if the group metric does not
// record the answer, or if a timed-out group does not fall to its failure
// destination.
func TestRingGroupsIntegration(t *testing.T) {
	vm := newVMStore()
	vm.boxes["100"] = VoicemailBox{ID: 7, Extension: "100"}
	objs := &fakeObjects{}
	groups := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "100", VoicemailEnabled: true, VoicemailBoxID: 7}})
		g := snapshot.RingGroup{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 30, MemberDelay: 2,
			FailureKind: "voicemail", FailureTarget: "100"}
		s.WithRingGroups([]snapshot.RingGroup{g}, [][]snapshot.RingGroupMember{
			{{ExtensionID: 1, Extension: "100", Position: 1, Weight: 1}, {ExtensionID: 2, Extension: "200", Position: 2, Weight: 1}},
		})
	}
	pbx := startPBX(t, callerDevices(), shortRing(400*time.Millisecond), withFeatures(groups), withVoicemail(vm, objs))
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	// The group rings b (the caller's own extension 100 is never rung).
	r := waitCall(t, dial(t.Context(), a, "700"))
	if r.err != nil {
		t.Fatalf("group call: %v", r.err)
	}
	waitReq(t, b.invites, "group INVITE to 200")
	noReq(t, a.invites, 200*time.Millisecond, "INVITE to the caller's own extension")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_group_calls_total", map[string]string{"group": "700", "strategy": "ring-all", "result": "answered"}); v != 1 {
		t.Fatalf("group_calls{700,ring-all,answered} = %v", v)
	}
	// b never answers: after the timeout the failure destination (the
	// voicemail of 100) answers the caller with the anchor.
	b.setCallee(ringForever())
	r = waitCall(t, dial(t.Context(), a, "700"))
	if r.err != nil {
		t.Fatalf("group timeout call: %v", r.err)
	}
	ans := r.dcs.InviteResponse.Body()
	if !strings.Contains(string(ans), "m=audio") {
		t.Fatalf("failure destination did not answer with the anchor SDP: %q", ans)
	}
	hangup(t, r.dcs)
	_ = pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_group_calls_total", map[string]string{"group": "700", "strategy": "ring-all", "result": "answered"}); v != 2 {
		t.Fatalf("failure destination did not answer the call: %v", v)
	}
}

// TestHuntGroup fails if two members ring at once, or if the hunt does not
// advance to the next member on no-answer.
func TestHuntGroup(t *testing.T) {
	groups := func(s *snapshot.Snapshot) {
		g := snapshot.RingGroup{ID: 2, Name: "800", Strategy: "sequential", Hunt: true, RingTimeout: 30, MemberDelay: 1}
		s.WithRingGroups([]snapshot.RingGroup{g}, [][]snapshot.RingGroupMember{
			{{ExtensionID: 3, Extension: "300", Position: 1, Weight: 1}, {ExtensionID: 2, Extension: "200", Position: 2, Weight: 1}},
		})
	}
	pbx := startPBX(t, threeDevices(), withFeatures(groups))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	c.setCallee(ringForever())
	deadline := time.Now().Add(5 * time.Second)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatalf("hunt call: %v", r.err)
	}
	// Member 1 (300) rings first.
	waitReq(t, c.invites, "hunt INVITE to the first member")
	// Member 2 must not ring until member 1's delay is up; when it does,
	// member 1's leg is gone.
	waitReq(t, b.invites, "hunt INVITE to the second member")
	if time.Now().After(deadline) {
		t.Fatal("hunt took too long")
	}
	waitReq(t, c.cancels, "CANCEL to the first member")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestAttendedTransfer fails if the two original parties are not connected
// after the bridge, if the transferee's legs do not drop, or if the CDR
// linkage between the two original Call-IDs is missing.
func TestAttendedTransfer(t *testing.T) {
	pbx := startPBX(t, threeDevices())
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	// Call 1: a -> b, answered.
	r1 := waitCall(t, dial(t.Context(), a, "200"))
	if r1.err != nil {
		t.Fatalf("call 1: %v", r1.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	var dssAB *sipgo.DialogServerSession
	for d := range serversOf(b) {
		dssAB = d
	}
	// Call 2: b -> c, answered (b is the caller there).
	r2 := waitCall(t, dial(t.Context(), b, "300"))
	if r2.err != nil {
		t.Fatalf("call 2: %v", r2.err)
	}
	waitReq(t, c.invites, "INVITE to c")
	replaces := "sip:100@" + testDomain + "?Replaces=" + dssAB.InviteRequest.CallID().Value()
	if res := b.sendRefer(t, r2.dcs, "<"+replaces+">"); res.StatusCode != 202 {
		t.Fatalf("REFER = %d, want 202", res.StatusCode)
	}
	waitReq(t, b.notifies, "progress NOTIFY to the transferee")
	final := waitReq(t, b.notifies, "final NOTIFY to the transferee")
	if !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("final NOTIFY = %q", final.Body())
	}
	// Both original parties re-INVITEd to each other; the transferee drops.
	waitReq(t, a.reinvites, "re-INVITE to a with c's answer")
	waitReq(t, c.reinvites, "re-INVITE to c with a's offer")
	waitReq(t, b.byes, "BYE to the transferee's first dialog")
	waitReq(t, b.byes, "BYE to the transferee's second dialog")
	if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "attended", "result": "answered"}); v != 1 {
		t.Fatalf("transfers{attended,answered} = %v", v)
	}
	// CDR linkage: both closed CDRs name the other call.
	cd1 := pbx.nextCDR(t)
	cd2 := pbx.nextCDR(t)
	linked := func(r cdr.Record) bool {
		for _, s := range r.Trace {
			if strings.Contains(s.Text, "bridged with call") {
				return true
			}
		}
		return false
	}
	if !linked(cd1) || !linked(cd2) {
		t.Fatalf("attended CDRs without the bridge link: %+v / %+v", cd1.Trace, cd2.Trace)
	}
	hangup(t, r1.dcs)
	hangup(t, r2.dcs)
}

// rtpFeed sends RTP to the anchor: seconds of silence plus DTMF digits, so
// the voicemail record loop sees real media without a full phone stack.
type rtpFeed struct {
	conn  net.PacketConn
	dst   netip.AddrPort
	seq   uint16
	ts    uint32
	ssrc  uint32
	audio uint8 // the negotiated payload type
	dtmf  uint8
}

func startRTPFeed(t *testing.T, answer []byte) *rtpFeed {
	t.Helper()
	asdp, err := media.ParseAudioSDP(answer)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ip, _ := netip.ParseAddr(asdp.Address)
	return &rtpFeed{conn: conn, dst: netip.AddrPortFrom(ip, uint16(asdp.Port)), ssrc: 1234, audio: asdp.PayloadType, dtmf: asdp.DTMFPayloadType}
}

func (f *rtpFeed) packet(pt uint8, payload []byte, marker bool) {
	f.seq++
	pkt := media.BuildRTP(pt, f.seq, f.ts, f.ssrc, marker, payload)
	_, _ = f.conn.WriteTo(pkt, net.UDPAddrFromAddrPort(f.dst))
}

// silence sends n seconds of PCMU silence in 20 ms frames.
func (f *rtpFeed) silence(seconds float64) {
	n := int(seconds * 50)
	for i := 0; i < n; i++ {
		f.packet(f.audio, make([]byte, 160), false)
		f.ts += 160
		time.Sleep(20 * time.Millisecond) // real time: 50 frames a second
	}
}

// digit sends one RFC 2833 event: start, a few middle, then the end packet.
func (f *rtpFeed) digit(d byte) {
	event := func(end bool) []byte {
		// byte 1 carries the end-of-event flag in its top bit.
		flags := byte(10)
		if end {
			flags |= 0x80
		}
		return []byte{d, flags, 0x00, 0x9c} // 160*4 ticks of duration
	}
	f.ts += 160
	f.packet(f.dtmf, event(false), true)
	for i := 0; i < 3; i++ {
		f.packet(f.dtmf, event(false), false)
		time.Sleep(5 * time.Millisecond)
	}
	f.packet(f.dtmf, event(true), false)
	time.Sleep(20 * time.Millisecond)
}

// TestVoicemailLeaveMessage fails if the answer is not the anchor's, if no
// WAV lands in the audio store with a message row behind it, if '#' does not
// finish the recording, or if the MWI MESSAGE with the new counts is not
// sent.
func TestVoicemailLeaveMessage(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	objs := &fakeObjects{}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, callerDevices(), shortRing(200*time.Millisecond), withFeatures(feats), withVoicemail(vm, objs))
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("voicemail call: %v", r.err)
	}
	answer := string(r.dcs.InviteResponse.Body())
	if !strings.Contains(answer, "m=audio") || !strings.Contains(answer, "telephone-event") || strings.Contains(answer, "53631") {
		t.Fatalf("answer is not the anchor's: %q", answer)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.silence(2.5) // past the beep, comfortably over one second of talk
	feed.digit('#')
	// The message is stored and announced; then the application hangs up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := len(vm.snapshotMsgs()); got == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("message was not stored")
		}
		time.Sleep(20 * time.Millisecond)
	}
	msg := vm.snapshotMsgs()[0]
	if msg.DurationMs < 1000 || msg.Caller != "100" || msg.BoxID != 5 {
		t.Fatalf("message row = %+v", msg)
	}
	wav, ok := objs.snapshot()[msg.MinioObject]
	if !ok || !strings.HasPrefix(string(wav[:4]), "RIFF") {
		t.Fatalf("audio object missing or not a WAV: %q", msg.MinioObject)
	}
	if v := pbx.metric(t, "hello_voicemail_messages_total", map[string]string{"result": "stored"}); v != 1 {
		t.Fatalf("voicemail_messages{stored} = %v", v)
	}
	// MWI: the box owner's device gets the MESSAGE with the counts.
	m := waitReq(t, b.messages, "MWI MESSAGE")
	body := string(m.Body())
	if !strings.Contains(body, "Messages-Waiting: yes") || !strings.Contains(body, "Message-Account: sip:200@") || !strings.Contains(body, "Voice-Message: 1/1") {
		t.Fatalf("MWI body = %q", body)
	}
}

// snapshotMsgs copies the store's messages.
func (f *fakeVMStore) snapshotMsgs() []VoicemailMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]VoicemailMessage(nil), f.msgs...)
}

func (f *fakeObjects) snapshot() map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range f.objs {
		out[k] = v
	}
	return out
}

// TestVoicemailBusyAndDND fails if a busy or DND call with a box is not
// routed to voicemail at once (before any ring timeout), and if a DND
// extension without a box is not declined 603.
func TestVoicemailBusyAndDND(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5}
	objs := &fakeObjects{}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{
			{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5},
			{Number: "300", DND: true, VoicemailEnabled: true, VoicemailBoxID: 5},
		})
	}
	pbx := startPBX(t, threeDevices(), shortRing(3*time.Second), withFeatures(feats), withVoicemail(vm, objs))
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	// busy: answered well inside the 3s ring timeout.
	start := time.Now()
	b.setCallee(busy())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("busy voicemail: %v", r.err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("busy voicemail took %s; not immediate", elapsed)
	}
	if !strings.Contains(string(r.dcs.InviteResponse.Body()), "m=audio") {
		t.Fatalf("busy call was not anchored: %q", r.dcs.InviteResponse.Body())
	}
	hangup(t, r.dcs) // ends the recording take; the CDR follows
	pbx.nextCDR(t)
	// DND with a box: to voicemail at once.
	c.setCallee(ringForever())
	r = waitCall(t, dial(t.Context(), a, "300"))
	if r.err != nil {
		t.Fatalf("DND voicemail: %v", r.err)
	}
	if !strings.Contains(string(r.dcs.InviteResponse.Body()), "m=audio") {
		t.Fatalf("DND call was not anchored")
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "dnd"}); v != 1 {
		t.Fatalf("forwarded{dnd} = %v", v)
	}
}

// TestPresenceBLF fails if a subscribing phone does not get a NOTIFY on
// idle->ringing->on-call->idle, if the state is not published to the live
// state keyspace, or if the subscription does not expire.
func TestPresenceBLF(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	// a subscribes to extension 200 (b's).
	if res := a.subscribe(t, "200", 120); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE = %d", res.StatusCode)
	}
	first := waitReq(t, a.notifies, "initial NOTIFY")
	if !strings.Contains(string(first.Body()), `entity="sip:200@`) || !strings.Contains(string(first.Body()), "<state>terminated</state>") {
		t.Fatalf("initial NOTIFY = %q", first.Body())
	}
	if v := pbx.metric(t, "hello_presence_subscriptions", nil); v != 1 {
		t.Fatalf("presence_subscriptions = %v", v)
	}
	// idle -> ringing -> on-call -> idle, each NOTIFYing the subscriber.
	answer := make(chan struct{})
	b.setCallee(answerAfter(answer))
	ch := dial(t.Context(), a, "200")
	waitReq(t, b.invites, "INVITE to b")
	ring := waitReq(t, a.notifies, "ringing NOTIFY")
	if !strings.Contains(string(ring.Body()), "<state>early</state>") {
		t.Fatalf("ringing NOTIFY = %q", ring.Body())
	}
	close(answer)
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.acks, "ACK")
	confirm := waitReq(t, a.notifies, "on-call NOTIFY")
	if !strings.Contains(string(confirm.Body()), "<state>confirmed</state>") {
		t.Fatalf("on-call NOTIFY = %q", confirm.Body())
	}
	hangup(t, r.dcs)
	idle := waitReq(t, a.notifies, "idle NOTIFY after hangup")
	if !strings.Contains(string(idle.Body()), "<state>terminated</state>") {
		t.Fatalf("idle NOTIFY = %q", idle.Body())
	}
	// The published device state followed the call too.
	st, ok := pbx.fake.DeviceState("b1")
	if !ok || st.Extension != "200" || st.State != StateIdle {
		t.Fatalf("published state = %+v ok=%v", st, ok)
	}
	// Expiry: a short subscription ends with a terminated NOTIFY.
	if res := a.subscribe(t, "200", 1); res.StatusCode != 200 {
		t.Fatalf("short SUBSCRIBE = %d", res.StatusCode)
	}
	waitReq(t, a.notifies, "refresh NOTIFY") // the refreshed subscription's first NOTIFY
	expired := waitReq(t, a.notifies, "expiry terminated NOTIFY")
	if !strings.Contains(string(expired.GetHeader("Subscription-State").Value()), "terminated") {
		t.Fatalf("expiry NOTIFY Subscription-State = %q", expired.GetHeader("Subscription-State").Value())
	}
	// The earlier subscription is untouched: one expiring mid-life never
	// disturbs the others (spec edge case).
	eventually(t, "subscription counter drops to the survivor", func() bool {
		return pbx.metric(t, "hello_presence_subscriptions", nil) == 1
	})
}

// featureSnapshot seeds the default code set the control plane writes.
func featureSnapshot(s *snapshot.Snapshot) {
	s.WithFeatureCodes([]snapshot.FeatureCode{
		{Code: "*72", Action: ActionForwardAlways, Argument: "set"},
		{Code: "*73", Action: ActionForwardAlways, Argument: "clear"},
		{Code: "*90", Action: ActionForwardBusy, Argument: "set"},
		{Code: "*78", Action: ActionDNDOn},
		{Code: "*79", Action: ActionDNDOff},
		{Code: "*97", Action: ActionVoicemail},
		{Code: "*2", Action: ActionBlindTransfer},
		{Code: "##", Action: ActionAttendedTransfer},
	})
}

// TestFeatureCodes fails if a dialled or in-dialog code does not perform its
// action, if a set without its target is not refused, or if an unknown code
// is not left to routing.
func TestFeatureCodes(t *testing.T) {
	settings := &fakeSettings{}
	vm := newVMStore()
	vm.boxes["100"] = VoicemailBox{ID: 3, Extension: "100"}
	vm.pwOK[3] = "1234"
	objs := &fakeObjects{}
	feats := func(s *snapshot.Snapshot) {
		featureSnapshot(s)
		s.WithExtensionData([]snapshot.Extension{{Number: "100", VoicemailEnabled: true, VoicemailBoxID: 3}})
	}
	pbx := startPBX(t, threeDevices(), withFeatures(feats), withVoicemail(vm, objs),
		func(_ *Config, d *Deps) { d.Settings = settings })
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)

	// DND on and off, dialled as calls.
	r := waitCall(t, dial(t.Context(), a, "*78"))
	if r.err != nil {
		t.Fatalf("*78: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	ups := settings.got()
	if len(ups) != 1 || ups[0].DND == nil || !*ups[0].DND || ups[0].Extension != "100" {
		t.Fatalf("*78 update = %+v", ups)
	}
	r = waitCall(t, dial(t.Context(), a, "*79"))
	if r.err != nil {
		t.Fatalf("*79: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	ups = settings.got()
	if len(ups) != 2 || ups[1].DND == nil || *ups[1].DND {
		t.Fatalf("*79 update = %+v", ups)
	}
	// A forward set needs its target: *72 alone is refused, *72 with the
	// target queued, *73 clears.
	r = waitCall(t, dial(t.Context(), a, "*72"))
	if code := responseCode(r.err); code != 400 {
		t.Fatalf("*72 without target = %v, want 400", r.err)
	}
	pbx.nextCDR(t)
	r = waitCall(t, dial(t.Context(), a, "*72300"))
	if r.err != nil {
		t.Fatalf("*72300: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	r = waitCall(t, dial(t.Context(), a, "*73"))
	if r.err != nil {
		t.Fatalf("*73: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	ups = settings.got()
	if len(ups) != 4 || ups[2].ForwardAlways == nil || *ups[2].ForwardAlways != "300" || ups[3].ForwardAlways == nil || *ups[3].ForwardAlways != "" {
		t.Fatalf("forward updates = %+v", ups)
	}
	// An unknown code is not a feature code: routing rejects it.
	r = waitCall(t, dial(t.Context(), a, "*55"))
	if code := responseCode(r.err); code != 404 {
		t.Fatalf("unknown code = %v, want 404", r.err)
	}
	pbx.nextCDR(t)
}

// TestFeatureCodeInDialog fails if the DTMF codes do not act inside a call:
// *2 plus the target digits blind-transfers to the target.
func TestFeatureCodeInDialog(t *testing.T) {
	settings := &fakeSettings{}
	feats := func(s *snapshot.Snapshot) { featureSnapshot(s) }
	pbx := startPBX(t, threeDevices(), withFeatures(feats), func(_ *Config, d *Deps) { d.Settings = settings })
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
	}
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	c.setCallee(answerAfter(nil))
	// a (the caller) dials *2, then the target, then '#'.
	a.sendDTMF(t, r.dcs, "*")
	a.sendDTMF(t, r.dcs, "2")
	a.sendDTMF(t, r.dcs, "3")
	a.sendDTMF(t, r.dcs, "0")
	a.sendDTMF(t, r.dcs, "0")
	a.sendDTMF(t, r.dcs, "#")
	waitReq(t, c.invites, "blind transfer to 300 by DTMF")
	waitReq(t, a.notifies, "progress NOTIFY to the transferee")
	final := waitReq(t, a.notifies, "final NOTIFY to the transferee")
	if !strings.Contains(string(final.Body()), "200 OK") {
		t.Fatalf("final NOTIFY = %q", final.Body())
	}
	if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "blind", "result": "answered"}); v != 1 {
		t.Fatalf("transfers{blind,answered} = %v", v)
	}
}

// TestFeatureMetrics fails if the Phase 4 metric families do not move
// through their flows: a stored voicemail message leaves bytes on the
// storage gauge and the email series registered for the control plane.
func TestFeatureMetrics(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5}
	objs := &fakeObjects{}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, callerDevices(), shortRing(200*time.Millisecond), withFeatures(feats), withVoicemail(vm, objs))
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("voicemail call: %v", r.err)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.silence(2.5)
	feed.digit('#')
	eventually(t, "message stored", func() bool {
		return len(vm.snapshotMsgs()) == 1
	})
	if v := pbx.metric(t, "hello_voicemail_storage_bytes", nil); v <= 0 {
		t.Fatalf("voicemail_storage_bytes = %v after a stored message", v)
	}
	// The email series exists for the control plane's worker (0 here).
	if v := pbx.metric(t, "hello_voicemail_email_total", map[string]string{"result": "sent"}); v != 0 {
		t.Fatalf("voicemail_email{sent} = %v before any delivery", v)
	}
}

// TestVoicemailStorageRetry fails if a MinIO hiccup is not retried: the
// first two uploads fail, the third must land the message anyway.
func TestVoicemailStorageRetry(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5}
	objs := &fakeObjects{failPuts: 2}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, callerDevices(), shortRing(200*time.Millisecond), withFeatures(feats), withVoicemail(vm, objs))
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("voicemail call: %v", r.err)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.silence(2.5)
	feed.digit('#')
	eventually(t, "message stored after retries", func() bool {
		return len(vm.snapshotMsgs()) == 1
	})
}
