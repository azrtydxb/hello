package sip

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func responseCode(err error) int {
	var de *sipgo.ErrDialogResponse
	if errors.As(err, &de) {
		return de.Res.StatusCode
	}
	return 0
}

type callResult struct {
	dcs *sipgo.DialogClientSession
	err error
}

func dial(ctx context.Context, p *phone, ext string) <-chan callResult {
	ch := make(chan callResult, 1)
	go func() {
		dcs, err := p.call(ctx, ext)
		ch <- callResult{dcs, err}
	}()
	return ch
}

func waitCall(t *testing.T, ch <-chan callResult) callResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("call did not finish")
		return callResult{}
	}
}

func liveCalls(t *testing.T, pbx *testPBX) []livestate.Call {
	t.Helper()
	cs, err := pbx.state.(interface {
		Calls(context.Context) ([]livestate.Call, error)
	}).Calls(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func liveState(t *testing.T, pbx *testPBX, want string) func() bool {
	return func() bool {
		cs := liveCalls(t, pbx)
		return len(cs) == 1 && cs[0].State == want && cs[0].Node == "sip-test"
	}
}

func hangup(t *testing.T, dcs *sipgo.DialogClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := dcs.Bye(ctx); err != nil {
		t.Fatalf("BYE: %v", err)
	}
}

func ringAllDevices() []snapshot.Device {
	return []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1"), dev(3, "b2", "200", "pb2")}
}

func TestCallRingAllFakeState(t *testing.T) { testCallRingAll(t, nil) }

// TestCallRingAllValkey runs the same flow with the live call and bindings
// in Valkey.
func TestCallRingAllValkey(t *testing.T) {
	st, th, _ := valkeyState(t)
	testCallRingAll(t, func(_ *Config, d *Deps) { d.State, d.Throttle = st, th })
}

// testCallRingAll fails if a call to an extension with two devices, one
// registered twice, does not ring all three contacts, if the first answer
// does not win with SDP unchanged both ways, if the losers keep ringing, if
// the live call is not published ringing then connected and removed at the
// end, or if the CDR does not record the answered call.
func testCallRingAll(t *testing.T, opt pbxOpt) {
	var opts []pbxOpt
	if opt != nil {
		opts = append(opts, opt)
	}
	pbx := startPBX(t, ringAllDevices(), opts...)
	a := newPhone(t, pbx, "a1", "pa")
	b1, b2x, b2y := newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b2", "pb2"), newPhone(t, pbx, "b2", "pb2")
	for _, p := range []*phone{a, b1, b2x, b2y} {
		p.register(t)
	}
	answer := make(chan struct{})
	b2x.setCallee(answerAfter(answer))
	b1.setCallee(ringForever())
	b2y.setCallee(ringForever())

	res := dial(t.Context(), a, "200")
	var forked *sip.Request
	for _, p := range []*phone{b1, b2x, b2y} {
		inv := waitReq(t, p.invites, p.addr+" INVITE")
		if string(inv.Body()) != a.sdp {
			t.Fatalf("fork SDP changed:\n%q\nwant\n%q", inv.Body(), a.sdp)
		}
		if p == b2x {
			forked = inv
		}
	}
	noReq(t, a.invites, 100*time.Millisecond, "INVITE to the caller")
	eventually(t, "live call ringing", liveState(t, pbx, "ringing"))
	if v := pbx.metric(t, "hello_active_calls", nil); v != 1 {
		t.Fatalf("active calls while ringing = %v", v)
	}

	close(answer)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	if got := string(r.dcs.InviteResponse.Body()); got != b2x.sdp {
		t.Fatalf("answer SDP changed:\n%q\nwant\n%q", got, b2x.sdp)
	}
	waitReq(t, b1.cancels, "CANCEL to losing fork b1")
	waitReq(t, b2y.cancels, "CANCEL to losing fork b2 (second contact)")
	waitReq(t, b2x.acks, "ACK to the winner")
	eventually(t, "live call connected", liveState(t, pbx, "connected"))
	if c := liveCalls(t, pbx)[0]; c.From != "100" || c.To != "200" || c.AnsweredAt.IsZero() || c.SIPCallID != r.dcs.InviteRequest.CallID().Value() {
		t.Fatalf("live call = %+v", c)
	}

	time.Sleep(50 * time.Millisecond)
	hangup(t, r.dcs)
	if bye := waitReq(t, b2x.byes, "BYE to the callee"); bye.CallID().Value() != forked.CallID().Value() {
		t.Fatalf("BYE on the wrong dialog")
	}
	cd := pbx.nextCDR(t)
	if cd.FinalStatus != 200 || cd.TerminationSide != "caller" || cd.AnswerTime.IsZero() || cd.RingTime.IsZero() ||
		cd.BillableMs < 40 || cd.BillableMs > cd.DurationMs || cd.Source != "100" || cd.Destination != "200" || cd.SIPNode != "sip-test" {
		t.Fatalf("CDR = %+v", cd)
	}
	eventually(t, "live call removed", func() bool { return len(liveCalls(t, pbx)) == 0 })
	if v := pbx.metric(t, "hello_active_calls", nil); v != 0 {
		t.Fatalf("active calls after hangup = %v", v)
	}
	noReq(t, b1.byes, 100*time.Millisecond, "BYE to a fork that never answered")
	pbx.noCDR(t, 100*time.Millisecond)
}

// TestCallSimultaneous2xx fails if, when two forks answer at once, the
// caller is not connected to exactly one and the other is not ACKed and
// then sent BYE.
func TestCallSimultaneous2xx(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a := newPhone(t, pbx, "a1", "pa")
	b1, b2 := newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b2", "pb2")
	for _, p := range []*phone{a, b1, b2} {
		p.register(t)
	}
	go1 := make(chan struct{})
	b1.setCallee(answerAfter(go1))
	b2.setCallee(answerAfter(go1))
	res := dial(t.Context(), a, "200")
	waitReq(t, b1.invites, "b1 INVITE")
	waitReq(t, b2.invites, "b2 INVITE")
	close(go1) // both answer now
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatal(r.err)
	}
	winner, loser := b1, b2
	if string(r.dcs.InviteResponse.Body()) == b2.sdp {
		winner, loser = b2, b1
	}
	waitReq(t, loser.acks, "ACK to the second 2xx")
	waitReq(t, loser.byes, "BYE to the second 2xx")
	waitReq(t, winner.acks, "ACK to the winner")
	noReq(t, winner.byes, 200*time.Millisecond, "BYE to the winner")
	hangup(t, r.dcs)
	waitReq(t, winner.byes, "BYE to the winner after hangup")
	if cd := pbx.nextCDR(t); cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
	pbx.noCDR(t, 100*time.Millisecond)
}

// TestCallCancelRacing200 fails if, when the caller's CANCEL crosses the
// callee's 200, the call does not end cleanly: 487 to the caller, ACK and
// BYE to the answering callee, one CDR recording a cancelled call.
func TestCallCancelRacing200(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerOnCancel())
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, a, "200")
	waitReq(t, b.invites, "callee INVITE")
	waitRing(t, a)
	cancel()
	r := waitCall(t, res)
	if r.err == nil {
		t.Fatal("cancelled call connected")
	}
	if r.dcs.InviteResponse == nil || r.dcs.InviteResponse.StatusCode != 487 {
		t.Fatalf("caller final = %v, want 487", r.dcs.InviteResponse)
	}
	waitReq(t, b.acks, "ACK to the callee's crossing 200")
	waitReq(t, b.byes, "BYE to the callee's crossing 200")
	cd := pbx.nextCDR(t)
	if cd.FinalStatus != 487 || cd.TerminationSide != "caller" || !cd.AnswerTime.IsZero() || cd.BillableMs != 0 {
		t.Fatalf("CDR = %+v", cd)
	}
	if v := pbx.metric(t, "hello_calls_total", map[string]string{"result": ResultCancelled}); v != 1 {
		t.Fatalf("cancelled calls = %v", v)
	}
	pbx.noCDR(t, 200*time.Millisecond)
}

// TestCallCancelWhileRinging fails if a caller CANCEL does not cancel every
// fork and record a cancelled CDR.
func TestCallCancelWhileRinging(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b1, b2 := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b2", "pb2")
	for _, p := range []*phone{a, b1, b2} {
		p.register(t)
		p.setCallee(ringForever())
	}
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, a, "200")
	waitReq(t, b1.invites, "b1 INVITE")
	waitReq(t, b2.invites, "b2 INVITE")
	eventually(t, "ringing", liveState(t, pbx, "ringing"))
	time.Sleep(50 * time.Millisecond)
	cancel()
	waitCall(t, res)
	waitReq(t, b1.cancels, "CANCEL b1")
	waitReq(t, b2.cancels, "CANCEL b2")
	if cd := pbx.nextCDR(t); cd.FinalStatus != 487 || cd.TerminationSide != "caller" || cd.RingTime.IsZero() {
		t.Fatalf("CDR = %+v", cd)
	}
	eventually(t, "live call removed", func() bool { return len(liveCalls(t, pbx)) == 0 })
}

// TestCallByeFromCallee fails if a BYE from the callee does not end the
// caller's leg and record the callee as the terminating side.
func TestCallByeFromCallee(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	inv := waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")
	b.mu.Lock()
	dss := b.servers[inv.CallID().Value()]
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := dss.Bye(ctx); err != nil {
		t.Fatalf("callee BYE: %v", err)
	}
	if bye := waitReq(t, a.byes, "BYE to the caller"); bye.CallID().Value() != r.dcs.InviteRequest.CallID().Value() {
		t.Fatal("BYE to the caller on the wrong dialog")
	}
	if cd := pbx.nextCDR(t); cd.FinalStatus != 200 || cd.TerminationSide != "callee" || cd.AnswerTime.IsZero() {
		t.Fatalf("CDR = %+v", cd)
	}
}

// bareExtension is configured in every test PBX but has no enabled device.
const bareExtension = "599"

// TestCallFailureCodes fails if an unknown number, an unregistered
// extension, all-busy forks or a ring timeout produce a code other than 404,
// 480, 486 or 408, or if the timed-out forks are not cancelled.
func TestCallFailureCodes(t *testing.T) {
	devs := append(ringAllDevices(), dev(4, "c1", "300", "pc"), dev(5, "d1", "400", "pd"))
	pbx := startPBX(t, devs, func(c *Config, _ *Deps) { c.RingTimeout = time.Second })
	a := newPhone(t, pbx, "a1", "pa")
	b1, b2, d1 := newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b2", "pb2"), newPhone(t, pbx, "d1", "pd")
	for _, p := range []*phone{a, b1, b2, d1} {
		p.register(t)
	}
	b1.setCallee(busy())
	b2.setCallee(busy())
	d1.setCallee(ringForever())
	seen := map[string]int{}
	for _, tc := range []struct {
		ext, result, side string
		code              int
	}{
		{"999", ResultNotFound, "system", 404},
		{"300", ResultUnavailable, "system", 480},
		{bareExtension, ResultUnavailable, "system", 480},
		{"200", ResultBusy, "callee", 486},
		{"400", ResultNoAnswer, "system", 408},
	} {
		seen[tc.result]++
		want := seen[tc.result]
		t.Run(tc.ext, func(t *testing.T) {
			r := waitCall(t, dial(t.Context(), a, tc.ext))
			if got := responseCode(r.err); got != tc.code {
				t.Fatalf("call %s: %v (code %d), want %d", tc.ext, r.err, got, tc.code)
			}
			cd := pbx.nextCDR(t)
			if cd.FinalStatus != tc.code || cd.TerminationSide != tc.side || cd.Destination != tc.ext || !cd.AnswerTime.IsZero() {
				t.Fatalf("CDR = %+v", cd)
			}
			if v := pbx.metric(t, "hello_calls_total", map[string]string{"result": tc.result}); v != float64(want) {
				t.Fatalf("hello_calls_total{%s} = %v", tc.result, v)
			}
		})
	}
	waitReq(t, d1.cancels, "CANCEL to the fork after ring timeout")
}

// TestCallOwnExtension fails if calling one's own extension rings the
// calling device, or does not ring the extension's other device.
func TestCallOwnExtension(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "a2", "100", "pa2"), dev(3, "s1", "500", "ps")})
	a1, a2, s1 := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "a2", "pa2"), newPhone(t, pbx, "s1", "ps")
	for _, p := range []*phone{a1, a2, s1} {
		p.register(t)
	}
	r := waitCall(t, dial(t.Context(), a1, "100"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, a2.invites, "INVITE to the other device")
	noReq(t, a1.invites, 200*time.Millisecond, "INVITE to the calling device")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	// The only device of the extension calling it: nobody else to ring.
	if r := waitCall(t, dial(t.Context(), s1, "500")); responseCode(r.err) != 480 {
		t.Fatalf("self call with one device = %v, want 480", r.err)
	}
}

// TestCallReInviteAndUpdateRelay fails if an in-dialog re-INVITE or UPDATE
// is not relayed to the other leg with its body unchanged, and its response
// relayed back.
func TestCallReInviteAndUpdateRelay(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	inv := waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	hold := a.sdp + "a=sendonly\r\n"
	re := sip.NewRequest(sip.INVITE, r.dcs.InviteResponse.Contact().Address)
	re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	re.SetBody([]byte(hold))
	res, err := r.dcs.Do(ctx, re)
	if err != nil || res.StatusCode != 200 || string(res.Body()) != hold {
		t.Fatalf("re-INVITE = %v, %v", res, err)
	}
	if got := waitReq(t, b.reinvites, "relayed re-INVITE"); string(got.Body()) != hold {
		t.Fatalf("re-INVITE body changed: %q", got.Body())
	}
	ack := sip.NewRequest(sip.ACK, r.dcs.InviteResponse.Contact().Address)
	if err := r.dcs.WriteRequest(ack); err != nil {
		t.Fatal(err)
	}
	waitReq(t, b.acks, "relayed re-INVITE ACK")

	b.mu.Lock()
	dss := b.servers[inv.CallID().Value()]
	b.mu.Unlock()
	up := sip.NewRequest(sip.UPDATE, inv.Contact().Address)
	up.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	up.SetBody([]byte(b.sdp))
	res, err = dss.Do(ctx, up)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("UPDATE = %v, %v", res, err)
	}
	if got := waitReq(t, a.reinvites, "relayed UPDATE"); string(got.Body()) != b.sdp || got.Method != sip.UPDATE {
		t.Fatalf("relayed UPDATE = %s %q", got.Method, got.Body())
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestSIPMetrics fails if a REGISTER and a completed call do not move
// hello_sip_requests_total, hello_sip_responses_total, hello_calls_total and
// hello_sip_registrations.
func TestSIPMetrics(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	if v := pbx.metric(t, "hello_sip_requests_total", map[string]string{"method": "REGISTER"}); v != 4 {
		t.Fatalf("REGISTER requests = %v, want 4 (two challenged, two authenticated)", v)
	}
	eventually(t, "hello_sip_registrations = 2", func() bool { return pbx.metric(t, "hello_sip_registrations", nil) == 2 })
	ring := make(chan struct{})
	b.setCallee(answerAfter(ring))
	res := dial(t.Context(), a, "200")
	waitReq(t, b.invites, "callee INVITE")
	waitRing(t, a)
	close(ring)
	r := waitCall(t, res)
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	// The ACK to the 401 stays in the transaction layer; only the 2xx ACK
	// reaches the handler.
	for m, want := range map[string]float64{"INVITE": 2, "ACK": 1, "BYE": 1} {
		if v := pbx.metric(t, "hello_sip_requests_total", map[string]string{"method": m}); v != want {
			t.Errorf("requests{%s} = %v, want %v", m, v, want)
		}
	}
	for code, min := range map[string]float64{"200": 4, "401": 3, "180": 1} {
		if v := pbx.metric(t, "hello_sip_responses_total", map[string]string{"code": code}); v < min {
			t.Errorf("responses{%s} = %v, want >= %v", code, v, min)
		}
	}
	if v := pbx.metric(t, "hello_calls_total", map[string]string{"result": ResultAnswered}); v != 1 {
		t.Errorf("calls{answered} = %v", v)
	}
	if v := pbx.metric(t, "hello_active_calls", nil); v != 0 {
		t.Errorf("active calls = %v", v)
	}
	// Unregister: the gauge follows.
	if res := a.authDo(a.registerReq(0), AlgSHA256, "pa"); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	eventually(t, "hello_sip_registrations = 1", func() bool { return pbx.metric(t, "hello_sip_registrations", nil) == 1 })
}

// waitRing waits until the calling phone has heard 180 Ringing.
func waitRing(t *testing.T, p *phone) {
	t.Helper()
	for {
		select {
		case code := <-p.rings:
			if code == 180 {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("caller never heard 180")
		}
	}
}
