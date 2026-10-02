package sip

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// TestDigestReplayRefused fails if an Authorization header can be replayed
// (same nonce, cnonce and nc) to change a binding, or if a higher nc on the
// same nonce is refused.
func TestDigestReplayRefused(t *testing.T) {
	pbx := startPBX(t, ringAllDevices())
	p := newPhone(t, pbx, "a1", "pa")
	req := p.registerReq(300)
	chal := pickChallenge(t, p.do(req), AlgSHA256)
	cred := func(nc int, cnonce string) string {
		c, err := digest.Digest(chal, digest.Options{Method: "REGISTER", URI: req.Recipient.String(), Username: "a1", Password: "pa", Count: nc, Cnonce: cnonce})
		if err != nil {
			t.Fatal(err)
		}
		return c.String()
	}
	send := func(contact, auth string) *sip.Response {
		r := p.registerReq(300, "<"+contact+">")
		r.AppendHeader(sip.NewHeader("Authorization", auth))
		return p.do(r)
	}
	first := cred(1, "c0ffee")
	if res := send("sip:a1@127.0.0.1:7001", first); res.StatusCode != 200 {
		t.Fatalf("first use = %d", res.StatusCode)
	}
	// The captured header, replayed to point the AOR somewhere else.
	res := send("sip:a1@203.0.113.66:5060", first)
	if res.StatusCode != 401 || !pickChallenge(t, res, AlgSHA256).Stale {
		t.Fatalf("replay = %d, want 401 stale=true", res.StatusCode)
	}
	if _, ok := bindings(t, pbx, "sip:a1@"+testDomain)["sip:a1@203.0.113.66:5060"]; ok {
		t.Fatal("replayed REGISTER changed the binding")
	}
	if res := send("sip:a1@127.0.0.1:7001", cred(2, "c0ffee")); res.StatusCode != 200 {
		t.Fatalf("nc=2 = %d, want 200", res.StatusCode)
	}
	for _, nc := range []int{2, 1} {
		if res := send("sip:a1@127.0.0.1:7001", cred(nc, "c0ffee")); res.StatusCode != 401 {
			t.Fatalf("reused nc=%d = %d, want 401", nc, res.StatusCode)
		}
	}
	if v := pbx.throttle.(*fakeThrottle); v.n["127.0.0.1"] != 0 {
		t.Fatalf("replays counted as failed attempts: %d", v.n["127.0.0.1"])
	}
	// The nonce-count store unreachable: 503, never accepted unchecked.
	pbx.throttle.(*fakeThrottle).ncDown.Store(true)
	if res := send("sip:a1@127.0.0.1:7001", cred(3, "c0ffee")); res.StatusCode != 503 {
		t.Fatalf("nc store down = %d, want 503", res.StatusCode)
	}
}

// TestValkeyNonceCount fails if the Valkey compare-and-set accepts a
// repeated or lower nc, refuses a higher one, or leaves no TTL.
func TestValkeyNonceCount(t *testing.T) {
	_, th, c := valkeyState(t)
	ctx := context.Background()
	for i, tc := range []struct {
		nc   int64
		want bool
	}{{1, true}, {1, false}, {0, false}, {3, true}, {2, false}, {4, true}} {
		if got, err := th.AdvanceNonceCount(ctx, "k", tc.nc, time.Minute); err != nil || got != tc.want {
			t.Fatalf("step %d nc=%d: %v, %v; want %v", i, tc.nc, got, err, tc.want)
		}
	}
	if ttl, _ := c.Do(ctx, c.B().Pttl().Key("hello:digestnc:k").Build()).AsInt64(); ttl <= 0 {
		t.Fatalf("nonce count TTL = %d", ttl)
	}
}

// TestAuthFailThrottleParallel fails if concurrent bad attempts that all
// pass the precheck are all rechallenged. Guarantee: in one window at most
// AuthFailLimit failed attempts from one IP get 401; the rest get 403.
func TestAuthFailThrottleParallel(t *testing.T) {
	const limit, n = 3, 12
	pbx := startPBX(t, ringAllDevices(), func(c *Config, _ *Deps) { c.AuthFailLimit = limit })
	p := newPhone(t, pbx, "a1", "pa")
	chal := pickChallenge(t, p.do(p.registerReq(300)), AlgSHA256)
	pbx.throttle.(*fakeThrottle).barrier.Store(newBarrier(n))
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			r := p.registerReq(300)
			c, err := digest.Digest(chal, digest.Options{Method: "REGISTER", URI: r.Recipient.String(), Username: "a1", Password: "wrong"})
			if err != nil {
				t.Error(err)
				return
			}
			r.AppendHeader(sip.NewHeader("Authorization", c.String()))
			codes <- p.do(r).StatusCode
		})
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[401] > limit || got[401]+got[403] != n {
		t.Fatalf("responses = %v; want at most %d 401s, the rest 403", got, limit)
	}
}

// TestRedactFoldedAndFlow fails if a folded Authorization header keeps its
// continuation lines, or an hflow token survives redaction.
func TestRedactFoldedAndFlow(t *testing.T) {
	msg := "INVITE sip:b@x SIP/2.0\r\nAuthorization: Digest username=\"a\",\r\n response=\"deadbeef\",\r\n\tnonce=\"n0nce\"\r\n" +
		"Route: <sip:10.0.0.1:5060;lr;hflow=TOKENVALUE123>\r\nRecord-Route: <sip:n:5060;hflow=OTHERTOKEN;lr>\r\nCall-ID: keep-me\r\n\r\n"
	got := RedactSIP(msg)
	for _, secret := range []string{"deadbeef", "n0nce", "TOKENVALUE123", "OTHERTOKEN"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted message still contains %q:\n%s", secret, got)
		}
	}
	for _, keep := range []string{"Call-ID: keep-me", "hflow=REDACTED", ";lr>", "Authorization: REDACTED"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("redaction lost %q:\n%s", keep, got)
		}
	}
}

// TestEndedCallNotResurrected fails if a heartbeat publish in flight when
// the call ends can write the live call back after it was deleted.
func TestEndedCallNotResurrected(t *testing.T) {
	pbx := startPBX(t, ringAllDevices(), func(c *Config, _ *Deps) { c.CallHeartbeat = 30 * time.Millisecond })
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.acks, "callee ACK")
	gate := make(chan struct{})
	pbx.fake.putGate.Store(&gate)
	select {
	case <-pbx.fake.putBlocked: // a heartbeat publish is now in flight
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat publish")
	}
	hangup(t, r.dcs)
	time.Sleep(150 * time.Millisecond) // let the hangup's delete run first if it can
	pbx.fake.putGate.Store(nil)
	close(gate)
	pbx.nextCDR(t)
	time.Sleep(200 * time.Millisecond)
	if cs := liveCalls(t, pbx); len(cs) != 0 {
		t.Fatalf("ended call resurrected in live state: %+v", cs)
	}
}
