package sip

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
	"strings"
)

// fakeOriginates is the click-to-call queue and state store in memory.
type fakeOriginates struct {
	mu        sync.Mutex
	queue     []livestate.OriginateRequest
	states    map[string][]livestate.OriginateState
	cancelled map[string]bool
	released  []string
}

func newFakeOriginates() *fakeOriginates {
	return &fakeOriginates{states: map[string][]livestate.OriginateState{}, cancelled: map[string]bool{}}
}

func (f *fakeOriginates) TakeOriginate(context.Context) (livestate.OriginateRequest, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return livestate.OriginateRequest{}, false, nil
	}
	r := f.queue[0]
	f.queue = f.queue[1:]
	return r, true, nil
}

func (f *fakeOriginates) PutOriginateState(_ context.Context, st livestate.OriginateState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[st.ID] = append(f.states[st.ID], st)
	return nil
}

func (f *fakeOriginates) OriginateCancelled(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled[id], nil
}

func (f *fakeOriginates) ReleaseOriginateSetup(_ context.Context, ext, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, ext+"/"+id)
	return nil
}

func (f *fakeOriginates) enqueue(r livestate.OriginateRequest) {
	f.mu.Lock()
	f.queue = append(f.queue, r)
	f.mu.Unlock()
}

func (f *fakeOriginates) cancel(id string) {
	f.mu.Lock()
	f.cancelled[id] = true
	f.mu.Unlock()
}

// seq is the states a request went through, in order.
func (f *fakeOriginates) seq(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.states[id] {
		out = append(out, s.State)
	}
	return out
}

func (f *fakeOriginates) waitState(t *testing.T, id, state string) livestate.OriginateState {
	t.Helper()
	eventually(t, "originate state "+state, func() bool { return slices.Contains(f.seq(id), state) })
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.states[id] {
		if s.State == state {
			return s
		}
	}
	return livestate.OriginateState{}
}

func withOriginates(f *fakeOriginates, ring time.Duration) pbxOpt {
	return func(c *Config, d *Deps) {
		d.Originates = f
		c.OriginateRing = ring
	}
}

// origEnv is a PBX with a user's phone (extension 100, a1) and a colleague's
// (200, b1), both registered.
type origEnv struct {
	pbx  *testPBX
	orig *fakeOriginates
	ev   *fakeEvents
	a, b *phone
}

func newOrigEnv(t *testing.T, ring time.Duration, opts ...pbxOpt) *origEnv {
	t.Helper()
	e := &origEnv{orig: newFakeOriginates(), ev: &fakeEvents{}}
	opts = append([]pbxOpt{withOriginates(e.orig, ring), withEvents(e.ev)}, opts...)
	e.pbx = startPBX(t, ringAllDevices(), opts...)
	e.a, e.b = newPhone(t, e.pbx, "a1", "pa"), newPhone(t, e.pbx, "b1", "pb1")
	e.a.register(t)
	e.b.register(t)
	return e
}

func (e *origEnv) request(id, dest string) livestate.OriginateRequest {
	rq := livestate.OriginateRequest{ID: id, Extension: "100", Destination: dest, Client: "token:7", Actor: "alice", CreatedAt: time.Now()}
	e.orig.enqueue(rq)
	return rq
}

// hangupFromPhone ends the call from p's side of the dialog it answered.
func (p *phone) hangupAnswered(t *testing.T, callID string) {
	t.Helper()
	p.mu.Lock()
	dss := p.servers[callID]
	p.mu.Unlock()
	if dss == nil {
		t.Fatalf("%s has no dialog %s", p.user, callID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := dss.Bye(ctx); err != nil {
		t.Fatalf("%s BYE: %v", p.user, err)
	}
}

// byeUnacked sends the BYE of a phone that hangs up before the ACK of its 200
// (click-to-call acknowledges it only when the destination answers); it
// returns the response. The tags come from the PBX's own dialog: the phone's
// sipgo objects are still being written by its goroutines.
func (p *phone) byeUnacked(pbx *testPBX, callID string) *sip.Response {
	p.t.Helper()
	ref, ok := pbx.srv.lookup(callID)
	if !ok {
		p.t.Fatalf("the PBX has no dialog %s", callID)
	}
	c := ref.c
	remoteTag, localTag := tagParam(c.inv.From()), tagParam(c.dss.InviteRequest.To())
	req := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", User: "200", Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: p.aorURI(), Params: sip.HeaderParams{{K: "tag", V: remoteTag}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: "200", Host: testDomain}, Params: sip.HeaderParams{{K: "tag", V: localTag}}})
	cid := sip.CallIDHeader(callID)
	req.AppendHeader(&cid)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 2, MethodName: sip.BYE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.SetDestination(p.pbx)
	return p.do(req)
}

// TestOriginateRingsFirstThenDials fails if click-to-call does not ring the
// user's phone first with the destination as the caller, if it dials the
// destination before the phone answered, if the answer is not carried to the
// phone as the ACK of its 200, if the states do not run queued-to-connected,
// if the phone's hangup does not end the destination's leg, or if the CDR
// does not say it came from the API and from whom.
func TestOriginateRingsFirstThenDials(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	answerA := make(chan struct{})
	e.a.setCallee(answerAfter(answerA))
	e.request("o1", "200")

	inv := waitReq(t, e.a.invites, "leg A INVITE")
	if len(inv.Body()) != 0 {
		t.Fatalf("leg A INVITE carries SDP %q, want a late offer", inv.Body())
	}
	if f := inv.From(); f.DisplayName != "Call to Ext 200" || f.Address.User != "200" {
		t.Fatalf("leg A caller = %q <%s>, want the destination", f.DisplayName, f.Address.String())
	}
	e.orig.waitState(t, "o1", livestate.OriginateRingingAgent)
	noReq(t, e.b.invites, 400*time.Millisecond, "destination INVITE before the phone answered")

	close(answerA)
	e.orig.waitState(t, "o1", livestate.OriginateDialingTarget)
	waitReq(t, e.b.invites, "destination INVITE")
	ack := waitReq(t, e.a.acks, "ACK of the phone's 200")
	if len(ack.Body()) == 0 {
		t.Fatal("the phone's ACK carries no SDP answer")
	}
	st := e.orig.waitState(t, "o1", livestate.OriginateConnected)
	if st.CorrelationID == "" {
		t.Fatal("connected state has no CDR correlation id")
	}
	if got := e.orig.seq("o1"); !slices.Equal(got, []string{"ringing_agent", "agent_answered", "dialing_target", "connected"}) {
		t.Fatalf("states = %v", got)
	}

	e.a.hangupAnswered(t, inv.CallID().Value())
	waitReq(t, e.b.byes, "BYE to the destination")
	e.orig.waitState(t, "o1", livestate.OriginateEnded)
	r := e.pbx.nextCDR(t)
	if r.Origin != cdr.OriginAPI || r.OriginClient != "token:7" || r.Source != "100" || r.Destination != "200" {
		t.Fatalf("CDR = origin %q client %q source %q destination %q", r.Origin, r.OriginClient, r.Source, r.Destination)
	}
	if r.CorrelationID != st.CorrelationID {
		t.Fatalf("CDR correlation id %q, state says %q", r.CorrelationID, st.CorrelationID)
	}
	eventually(t, "progress events", func() bool { return len(e.ev.of(events.CallOriginateProgress)) >= 5 })
	if ev, ok := e.ev.first(events.CallOriginateProgress, "100"); !ok || ev.Data.OriginateID != "o1" || ev.Data.State != "ringing_agent" {
		t.Fatalf("first progress event = %+v", ev.Data)
	}
	if !slices.Contains(e.orig.released, "100/o1") {
		t.Fatalf("setup slot not released: %v", e.orig.released)
	}
}

// failedWith waits for request id to fail and returns the reason.
func (e *origEnv) failedWith(t *testing.T, id string) string {
	t.Helper()
	return e.orig.waitState(t, id, livestate.OriginateFailed).Reason
}

// TestOriginateAgentNoAnswerDialsNothing fails if a phone that rings out does
// not fail the request agent_no_answer, or if the destination is rung
// anyway, or if a CDR is written for a call that never connected.
func TestOriginateAgentNoAnswerDialsNothing(t *testing.T) {
	e := newOrigEnv(t, 600*time.Millisecond)
	e.a.setCallee(ringForever())
	e.request("o1", "200")
	waitReq(t, e.a.invites, "leg A INVITE")
	if got := e.failedWith(t, "o1"); got != livestate.OriginateAgentNoAnswer {
		t.Fatalf("reason = %q, want agent_no_answer", got)
	}
	noReq(t, e.b.invites, 300*time.Millisecond, "destination INVITE")
	e.pbx.noCDR(t, 200*time.Millisecond)
	if got := e.orig.seq("o1"); !slices.Equal(got, []string{"ringing_agent", "failed"}) {
		t.Fatalf("states = %v", got)
	}
}

// TestOriginateAgentRefusals fails if a busy phone, a phone on DND, an
// extension with no registration, or a destination the routing refuses is
// not reported with its own reason before anything rings.
func TestOriginateAgentRefusals(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second, withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "100", Name: "Ext 100"}, {Number: "200", Name: "Ext 200", DND: true}})
		s.WithFeatureCodes([]snapshot.FeatureCode{{Code: "*72", Action: ActionIntercom}})
	}))
	e.a.setCallee(busy())
	e.request("busy", "200")
	waitReq(t, e.a.invites, "leg A INVITE")
	if got := e.failedWith(t, "busy"); got != livestate.OriginateAgentBusy {
		t.Fatalf("busy phone: reason %q", got)
	}
	for _, c := range []struct{ id, dest, want string }{
		{"nowhere", "9999", livestate.OriginateRouteDenied}, // no route: refused before ringing
		{"code", "*72", livestate.OriginateRouteDenied},     // a feature code is not a destination
		{"uri", "sip:evil@host", livestate.OriginateRouteDenied},
	} {
		e.request(c.id, c.dest)
		if got := e.failedWith(t, c.id); got != c.want {
			t.Fatalf("%s: reason %q, want %q", c.id, got, c.want)
		}
	}
	noReq(t, e.a.invites, 200*time.Millisecond, "INVITE for a refused destination")

	// Extension 200 is on DND and has no registration for 300.
	rq := livestate.OriginateRequest{ID: "dnd", Extension: "200", Destination: "100"}
	e.orig.enqueue(rq)
	if got := e.failedWith(t, "dnd"); got != livestate.OriginateAgentDND {
		t.Fatalf("DND: reason %q", got)
	}
	e.orig.enqueue(livestate.OriginateRequest{ID: "unreg", Extension: "599", Destination: "100"})
	if got := e.failedWith(t, "unreg"); got != livestate.OriginateNoRegistration {
		t.Fatalf("no registration: reason %q", got)
	}
}

// TestOriginateEmergencyRefused fails if a route marked emergency can be
// dialled by click-to-call.
func TestOriginateEmergencyRefused(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	cfg := routing.Config{
		Trunks: []routing.Trunk{cr.trunk(1, "carrier", "ip")},
		Outbound: []routing.OutboundRoute{{ID: 1, Position: 1, Name: "Emergency", MatchKind: "prefix", Match: "999",
			Trunks: []int64{1}, Enabled: true, Emergency: true}},
		Extensions: map[string]string{"100": "", "200": ""},
	}
	e := newOrigEnv(t, 5*time.Second, trunkCfg(newFakeTrunkState()), withEngine(t, cfg))
	e.request("sos", "999")
	if got := e.failedWith(t, "sos"); got != livestate.OriginateRouteDenied {
		t.Fatalf("emergency: reason %q, want route_denied", got)
	}
	noReq(t, e.a.invites, 200*time.Millisecond, "INVITE for an emergency number")
}

// TestOriginateTargetBusyHangsUpThePhone fails if a busy destination is not
// reported target_busy, if the phone that answered is not hung up, or if the
// attempt is not in the CDR as an API call.
func TestOriginateTargetBusyHangsUpThePhone(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	e.b.setCallee(busy())
	e.request("o1", "200")
	inv := waitReq(t, e.a.invites, "leg A INVITE")
	if got := e.failedWith(t, "o1"); got != livestate.OriginateTargetBusy {
		t.Fatalf("reason = %q, want target_busy", got)
	}
	bye := waitReq(t, e.a.byes, "BYE to the phone")
	if bye.CallID().Value() != inv.CallID().Value() {
		t.Fatalf("BYE for call %s, want %s", bye.CallID().Value(), inv.CallID().Value())
	}
	r := e.pbx.nextCDR(t)
	if r.Origin != cdr.OriginAPI || r.FinalStatus != 486 {
		t.Fatalf("CDR origin %q status %d", r.Origin, r.FinalStatus)
	}
}

// TestOriginateCancelWhileRinging fails if DELETE while the phone rings does
// not CANCEL its INVITE and end the request cancelled without dialling.
func TestOriginateCancelWhileRinging(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	e.a.setCallee(ringForever())
	e.request("o1", "200")
	waitReq(t, e.a.invites, "leg A INVITE")
	e.orig.waitState(t, "o1", livestate.OriginateRingingAgent)
	e.orig.cancel("o1")
	waitReq(t, e.a.cancels, "CANCEL of leg A")
	if got := e.failedWith(t, "o1"); got != livestate.OriginateCancelled {
		t.Fatalf("reason = %q, want cancelled", got)
	}
	noReq(t, e.b.invites, 200*time.Millisecond, "destination INVITE")
}

// TestOriginatePhoneHangsUpWhileTargetRings fails if the user hanging up
// before the destination answers does not cancel the destination's ring.
func TestOriginatePhoneHangsUpWhileTargetRings(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	e.b.setCallee(ringForever())
	e.request("o1", "200")
	inv := waitReq(t, e.a.invites, "leg A INVITE")
	waitReq(t, e.b.invites, "destination INVITE")
	e.orig.waitState(t, "o1", livestate.OriginateDialingTarget)
	if res := e.a.byeUnacked(e.pbx, inv.CallID().Value()); res.StatusCode != 200 {
		t.Fatalf("BYE before the destination answered = %d, want 200", res.StatusCode)
	}
	waitReq(t, e.b.cancels, "CANCEL of the destination")
	if got := e.failedWith(t, "o1"); got != livestate.OriginateCancelled {
		t.Fatalf("reason = %q, want cancelled", got)
	}
}

// TestOriginateHangupConnected fails if DELETE on a connected click-to-call
// does not end both legs and the request.
func TestOriginateHangupConnected(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	e.request("o1", "200")
	waitReq(t, e.a.invites, "leg A INVITE")
	e.orig.waitState(t, "o1", livestate.OriginateConnected)
	e.orig.cancel("o1")
	waitReq(t, e.a.byes, "BYE to the phone")
	waitReq(t, e.b.byes, "BYE to the destination")
	e.orig.waitState(t, "o1", livestate.OriginateEnded)
}

// TestOriginateDestinationHangsUp fails if the destination hanging up does
// not end the phone's dialog (the BYE must use the dialog our INVITE made)
// and the request.
func TestOriginateDestinationHangsUp(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	e.request("o1", "200")
	waitReq(t, e.a.invites, "leg A INVITE")
	binv := waitReq(t, e.b.invites, "destination INVITE")
	e.orig.waitState(t, "o1", livestate.OriginateConnected)
	waitReq(t, e.b.acks, "destination ACK")
	e.b.hangupAnswered(t, binv.CallID().Value())
	waitReq(t, e.a.byes, "BYE to the phone")
	e.orig.waitState(t, "o1", livestate.OriginateEnded)
}

// TestOriginateHoldRelayed fails if a re-INVITE from the destination (hold)
// does not reach the phone: the engine must find the phone's dialog.
func TestOriginateHoldRelayed(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	e.request("o1", "200")
	waitReq(t, e.a.invites, "leg A INVITE")
	binv := waitReq(t, e.b.invites, "destination INVITE")
	e.orig.waitState(t, "o1", livestate.OriginateConnected)
	waitReq(t, e.a.acks, "phone ACK")
	e.b.mu.Lock()
	dss := e.b.servers[binv.CallID().Value()]
	e.b.mu.Unlock()
	hold := strings.Replace(e.b.sdp, "m=audio", "a=sendonly\r\nm=audio", 1)
	req := sip.NewRequest(sip.INVITE, *binv.Contact().Address.Clone())
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(hold))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	res, err := dss.Do(ctx, req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %v, %v", res, err)
	}
	waitReq(t, e.a.reinvites, "re-INVITE at the phone")
}

// TestOriginateAutoAnswerHeaders fails if autoAnswer does not add the vendor's
// auto-answer headers to leg A, or adds them when it was not asked for.
func TestOriginateAutoAnswerHeaders(t *testing.T) {
	e := newOrigEnv(t, 5*time.Second)
	rq := e.request("o1", "200")
	inv := waitReq(t, e.a.invites, "leg A INVITE")
	if inv.GetHeader("Call-Info") != nil || inv.GetHeader("Alert-Info") != nil {
		t.Fatal("a plain click-to-call carries auto-answer headers")
	}
	e.orig.waitState(t, rq.ID, livestate.OriginateConnected)
	e.orig.cancel("o1")
	e.orig.waitState(t, rq.ID, livestate.OriginateEnded)

	rq2 := livestate.OriginateRequest{ID: "o2", Extension: "100", Destination: "200", AutoAnswer: true}
	e.orig.enqueue(rq2)
	inv2 := waitReq(t, e.a.invites, "second leg A INVITE")
	if h := inv2.GetHeader("Call-Info"); h == nil || !strings.Contains(h.Value(), "answer-after=0") {
		t.Fatalf("auto-answer INVITE has no Call-Info answer-after: %v", inv2.GetHeader("Call-Info"))
	}
}

// TestOriginateRestrictedDestinations fails if the administrator's
// restriction does not refuse an outbound number outside the allowed
// prefixes, or refuses one inside them.
func TestOriginateRestrictedDestinations(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	cfg := routing.Config{
		Trunks: []routing.Trunk{cr.trunk(1, "carrier", "ip")},
		Outbound: []routing.OutboundRoute{{ID: 1, Position: 1, Name: "Out", MatchKind: "prefix", Match: "0",
			Trunks: []int64{1}, Enabled: true}},
		Extensions: map[string]string{"100": "", "200": ""},
	}
	e := newOrigEnv(t, 5*time.Second, trunkCfg(newFakeTrunkState()), withEngine(t, cfg))
	e.orig.enqueue(livestate.OriginateRequest{ID: "no", Extension: "100", Destination: "0501234567", Restrict: true, Prefixes: []string{"+44"}})
	if got := e.failedWith(t, "no"); got != livestate.OriginateRouteDenied {
		t.Fatalf("outside the prefixes: reason %q", got)
	}
	noReq(t, e.a.invites, 200*time.Millisecond, "INVITE for a restricted number")
	e.orig.enqueue(livestate.OriginateRequest{ID: "yes", Extension: "100", Destination: "0501234567", Restrict: true, Prefixes: []string{"05"}})
	waitReq(t, e.a.invites, "leg A INVITE inside the prefixes")
	waitReq(t, cr.invites, "INVITE to the carrier")
	e.orig.waitState(t, "yes", livestate.OriginateConnected)
	// Internal numbers are never restricted.
	e.orig.cancel("yes")
	e.orig.waitState(t, "yes", livestate.OriginateEnded)
}
