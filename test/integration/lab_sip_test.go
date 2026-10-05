package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/test/sipua"
)

// Lab-level SIP tests (spec .procoder/specs/minimum-pbx.md). Callers and
// callees register through the same node unless the test is about crossing
// nodes, so each test isolates one behaviour.

// secrets collects every secret the lab tests handle, for TestNoSecretsInLogs.
var secrets struct {
	sync.Mutex
	values []string
}

func remember(vals ...string) {
	secrets.Lock()
	defer secrets.Unlock()
	secrets.values = append(secrets.values, vals...)
}

// devices provisions an extension with the named devices and remembers
// their secrets.
func (lc *labClient) devices(names ...string) []labDevice {
	lc.t.Helper()
	ds := lc.extension(names...)
	for _, d := range ds {
		remember(d.Secret)
	}
	return ds
}

func (lc *labClient) registrations() []livestate.Binding {
	lc.t.Helper()
	var out struct{ Items []livestate.Binding }
	lc.must("GET", "/api/v1/registrations", nil, &out, 200)
	return out.Items
}

func (lc *labClient) calls() []livestate.Call {
	lc.t.Helper()
	var out struct{ Items []livestate.Call }
	lc.must("GET", "/api/v1/calls", nil, &out, 200)
	return out.Items
}

type labCDR struct {
	SIPCallID       string    `json:"sipCallId"`
	Source          string    `json:"source"`
	Destination     string    `json:"destination"`
	AnswerTime      time.Time `json:"answerTime"`
	BillableMs      int64     `json:"billableMs"`
	FinalStatus     int       `json:"finalStatus"`
	TerminationSide string    `json:"terminationSide"`
	SIPNode         string    `json:"sipNode"`
}

func (lc *labClient) cdrsTo(dest string) []labCDR {
	lc.t.Helper()
	var out struct{ Items []labCDR }
	lc.must("GET", "/api/v1/cdrs?limit=200", nil, &out, 200)
	var mine []labCDR
	for _, c := range out.Items {
		if c.Destination == dest {
			mine = append(mine, c)
		}
	}
	return mine
}

func bindingsFor(bs []livestate.Binding, device string) []livestate.Binding {
	var out []livestate.Binding
	for _, b := range bs {
		if b.Device == device {
			out = append(out, b)
		}
	}
	return out
}

// eventually polls f until it returns nil or d elapses.
func eventually(t *testing.T, d time.Duration, what string, f func() error) {
	t.Helper()
	eventuallyBy(t, time.Now().Add(d), what, func(context.Context) error { return f() })
}

// eventuallyBy polls f until it returns nil, failing the test unless that
// happens by deadline: each attempt's context ends at the deadline, and a
// success that only arrives after it is a failure too.
func eventuallyBy(t *testing.T, deadline time.Time, what string, f func(ctx context.Context) error) {
	t.Helper()
	var last error
	for {
		if time.Until(deadline) <= 0 {
			t.Fatalf("%s: not by the deadline: %v", what, last)
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		err := f(ctx)
		cancel()
		if err == nil {
			if late := time.Since(deadline); late > 0 {
				t.Fatalf("%s: succeeded %s after the deadline", what, late)
			}
			return
		}
		last = err
		time.Sleep(min(250*time.Millisecond, max(time.Until(deadline), 0)))
	}
}

func labCompose(t *testing.T, args ...string) {
	t.Helper()
	if out, err := compose(args...).CombinedOutput(); err != nil {
		t.Fatalf("compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// answerNext answers the next call to p, after ringing, with sdpAnswer.
func answerNext(ctx context.Context, p *sipua.Phone) <-chan *sipua.Incoming {
	ch := make(chan *sipua.Incoming, 1)
	go func() {
		in, err := p.Next(ctx)
		if err != nil {
			close(ch)
			return
		}
		_ = in.Ring()
		_ = in.Answer(sdpAnswer)
		ch <- in
	}()
	return ch
}

func TestRegisterBindings(t *testing.T) {
	lc := newLabClient(t)
	d := lc.devices("desk")[0]
	p1, p2 := phone(t, d, labSIP1), phone(t, d, labSIP1) // two contacts, one AOR
	register(t, p1)
	register(t, p2)
	if got := bindingsFor(lc.registrations(), d.User); len(got) != 2 {
		t.Fatalf("bindings after two contacts = %+v, want 2", got)
	}

	// Refresh p1's contact through the other node: overwrite, not add.
	p1b := phone(t, d, labSIP2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if res, err := p1b.RegisterContact(ctx, p1.ContactString(), time.Hour); err != nil || res.StatusCode != 200 {
		t.Fatalf("refresh via other node = %v, %v", res, err)
	}
	got := bindingsFor(lc.registrations(), d.User)
	if len(got) != 2 {
		t.Fatalf("bindings after cross-node refresh = %+v, want still 2", got)
	}

	// Expires: 0 removes exactly that binding.
	if res, err := p2.Register(ctx, 0); err != nil || res.StatusCode != 200 {
		t.Fatalf("unregister = %v, %v", res, err)
	}
	if got := bindingsFor(lc.registrations(), d.User); len(got) != 1 || got[0].ContactURI != p1.ContactString() {
		t.Fatalf("bindings after Expires: 0 = %+v, want only p1", got)
	}
	// Contact: * removes the rest.
	if res, err := p1.UnregisterAll(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("unregister all = %v, %v", res, err)
	}
	if got := bindingsFor(lc.registrations(), d.User); len(got) != 0 {
		t.Fatalf("bindings after Contact: * = %+v", got)
	}
}

func TestCallRingAllAndHangup(t *testing.T) {
	lc := newLabClient(t)
	caller := lc.devices("desk")[0]
	callees := lc.devices("desk", "soft")
	a := phone(t, caller, labSIP1)
	b1, b2 := phone(t, callees[0], labSIP1), phone(t, callees[1], labSIP1)
	for _, p := range []*sipua.Phone{a, b1, b2} {
		register(t, p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// b1 answers after both ring; b2 rings and must be cancelled.
	b2rang := make(chan *sipua.Incoming, 1)
	go func() {
		if in, err := b2.Next(ctx); err == nil {
			_ = in.Ring()
			b2rang <- in
		}
	}()
	b1got := make(chan *sipua.Incoming, 1)
	go func() {
		in, err := b1.Next(ctx)
		if err != nil {
			return
		}
		_ = in.Ring()
		select {
		case l := <-b2rang:
			b2rang <- l
		case <-ctx.Done():
			return
		}
		_ = in.Answer(sdpAnswer)
		b1got <- in
	}()
	out, err := a.Dial(ctx, callees[0].Extension, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 200 {
		t.Fatalf("dial = %d %q, want 200", out.Status, out.Response.Body())
	}
	// The bodies: pass-through (direct) or the anchor's SDP (anchored) —
	// see sdpDirectOrAnchored in lab_test.go for why these calls anchor.
	if err := sdpDirectOrAnchored(sdpAnswer, out.Response.Body()); err != nil {
		t.Fatalf("dial media: %v", err)
	}
	in1 := <-b1got
	if err := sdpDirectOrAnchored(sdpOffer, in1.Request.Body()); err != nil {
		t.Fatalf("callee media: %v", err)
	}
	in2 := <-b2rang
	select {
	case <-in2.Canceled():
	case <-time.After(5 * time.Second):
		t.Fatal("losing device still ringing 5s after the other answered")
	}

	// BYE from the callee ends the caller's leg too.
	if err := in1.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("caller leg still up 5s after the callee hung up")
	}
}

func TestCallFailureCodes(t *testing.T) {
	lc := newLabClient(t)
	caller := lc.devices("desk")[0]
	unregistered := lc.devices("desk")[0]
	busyExt := lc.devices("desk")[0]
	slowExt := lc.devices("desk")[0]
	a := phone(t, caller, labSIP1)
	busy, slow := phone(t, busyExt, labSIP1), phone(t, slowExt, labSIP1)
	for _, p := range []*sipua.Phone{a, busy, slow} {
		register(t, p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() {
		if in, err := busy.Next(ctx); err == nil {
			_ = in.Reject(486, "Busy Here")
		}
	}()
	go func() {
		if in, err := slow.Next(ctx); err == nil {
			_ = in.Ring() // never answers: the lab's 10s ring timeout applies
		}
	}()
	bareNumber := "7" + randDigits(7) // an extension with no device at all
	lc.must("POST", "/api/v1/extensions", map[string]string{"number": bareNumber, "name": "bare"}, nil, 201)

	// A fresh extension has a voicemail box (Phase 4), so the failure
	// scenarios would be answered by the voicemail application. First prove
	// the hand-off: an unreachable extension with voicemail answers 200.
	// The dial reads the snapshot, which lags the extension's creation, so
	// the 200 may need a retry to become visible at all.
	eventually(t, 10*time.Second, "voicemail answers an unreachable extension", func() error {
		out, err := a.Dial(ctx, bareNumber, sdpOffer)
		if err != nil {
			return err
		}
		if err := out.Hangup(ctx); err != nil {
			return err
		}
		if out.Status != 200 {
			return fmt.Errorf("dial %s with voicemail = %d, want 200 (voicemail answers)", bareNumber, out.Status)
		}
		return nil
	})
	// Then silence voicemail on every involved extension, so the test can
	// assert the raw failure codes the features pass through.
	for _, number := range []string{unregistered.Extension, busyExt.Extension, slowExt.Extension, bareNumber} {
		lc.must("PATCH", "/api/v1/extensions/"+lc.extensionID(number),
			map[string]any{"voicemailEnabled": false}, nil, 200)
	}
	// The dials below read the snapshot, which lags the PATCH by the reload:
	// the fast bare dial is true again only once it landed.
	eventually(t, 10*time.Second, "voicemail disabled in the snapshot", func() error {
		out, err := a.Dial(ctx, bareNumber, sdpOffer)
		if err != nil {
			return err
		}
		if out.Status != 480 {
			_ = out.Hangup(ctx)
			return fmt.Errorf("dial %s = %d, want 480", bareNumber, out.Status)
		}
		return nil
	})
	for _, tc := range []struct {
		name, number string
		want         int
	}{
		{"unknown number", "6" + randDigits(7), 404},
		{"extension without devices", bareNumber, 480},
		{"device not registered", unregistered.Extension, 480},
		{"busy callee", busyExt.Extension, 486},
		{"ring timeout", slowExt.Extension, 408},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := a.Dial(ctx, tc.number, sdpOffer)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != tc.want {
				t.Fatalf("dial %s = %d, want %d", tc.number, out.Status, tc.want)
			}
		})
	}
}

func TestLiveRegistrationsAndCalls(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := phone(t, caller, labSIP2), phone(t, callee, labSIP2)
	register(t, a)
	register(t, b)
	if len(bindingsFor(lc.registrations(), callee.User)) != 1 {
		t.Fatal("registration missing from /api/v1/registrations")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	<-got
	callListed := func() bool {
		for _, c := range lc.calls() {
			if c.To == callee.Extension && c.State == "connected" && c.Node == "hello-sip-2" {
				return true
			}
		}
		return false
	}
	eventually(t, 5*time.Second, "connected call listed", func() error {
		if !callListed() {
			return errors.New("not listed")
		}
		return nil
	})

	// Kill the owning node: its call must disappear within the 30s TTL.
	t.Cleanup(func() { labCompose(t, "up", "-d", "--wait", "hello-sip-2") })
	labCompose(t, "kill", "hello-sip-2")
	eventually(t, 40*time.Second, "dead node's call removed", func() error {
		if callListed() {
			return errors.New("still listed")
		}
		return nil
	})
}

func TestCDRWritten(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := phone(t, caller, labSIP1), phone(t, callee, labSIP1)
	register(t, a)
	register(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Answered call, hung up by the caller after ~1s.
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	<-got
	time.Sleep(time.Second)
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}

	// Cancelled call: the caller gives up while the callee rings.
	go func() {
		if in, err := b.Next(ctx); err == nil {
			_ = in.Ring()
		}
	}()
	dctx, dcancel := context.WithTimeout(ctx, 2*time.Second)
	_, _ = a.Dial(dctx, callee.Extension, sdpOffer)
	dcancel()

	eventually(t, 10*time.Second, "two CDRs", func() error {
		cs := lc.cdrsTo(callee.Extension)
		if len(cs) != 2 {
			return fmt.Errorf("CDRs = %+v, want 2", cs)
		}
		// Newest first: cancelled, then answered.
		cancelled, answered := cs[0], cs[1]
		if answered.FinalStatus != 200 || answered.AnswerTime.IsZero() || answered.BillableMs < 900 || answered.TerminationSide != "caller" {
			return fmt.Errorf("answered CDR = %+v", answered)
		}
		if cancelled.FinalStatus != 487 || !cancelled.AnswerTime.IsZero() || cancelled.BillableMs != 0 || cancelled.TerminationSide != "caller" {
			return fmt.Errorf("cancelled CDR = %+v", cancelled)
		}
		return nil
	})
}

func TestSnapshotReloadOnNotify(t *testing.T) {
	lc := newLabClient(t)
	start := time.Now()
	d := lc.devices("desk")[0]
	p := phone(t, d, labSIP2)
	eventually(t, 2*time.Second, "new device registers within 2s of creation", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		res, err := p.Register(ctx, time.Hour)
		if err != nil {
			return err
		}
		if res.StatusCode != 200 {
			return fmt.Errorf("REGISTER = %d", res.StatusCode)
		}
		return nil
	})
	t.Logf("create to registered: %s", time.Since(start))
}

func TestSnapshotSurvivesDatabaseLoss(t *testing.T) {
	lc := newLabClient(t)
	d := lc.devices("desk")[0]
	// Both nodes must have the device before the database goes away.
	for _, node := range []string{labSIP1, labSIP2} {
		register(t, phone(t, d, node))
	}
	t.Cleanup(func() {
		labCompose(t, "start", "postgres")
		labCompose(t, "up", "-d", "--wait")
	})
	labCompose(t, "stop", "postgres")
	for _, node := range []string{labSIP1, labSIP2} {
		register(t, phone(t, d, node))
	}
}

// TestAuthFailThrottle runs near the end: the lab sees every host-side test
// phone as one source IP, so it clears the throttle counters afterwards.
func TestAuthFailThrottle(t *testing.T) {
	lc := newLabClient(t)
	d := lc.devices("desk")[0]
	t.Cleanup(func() { clearThrottle(t) })
	clearThrottle(t) // on the current Valkey primary (Sentinel)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nodes := []*sipua.Phone{phone(t, d, labSIP1), phone(t, d, labSIP2)}
	for i := range 10 {
		res, err := nodes[i%2].RegisterWithPassword(ctx, time.Hour, "wrong-"+randDigits(6))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == 200 {
			t.Fatalf("attempt %d with a wrong password succeeded", i+1)
		}
	}
	for _, p := range nodes {
		res, err := p.Probe(ctx, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 403 {
			t.Fatalf("after 10 failures, REGISTER = %d, want 403 without a challenge", res.StatusCode)
		}
	}
}
