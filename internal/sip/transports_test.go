package sip

import (
	"net/netip"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo/sip"
)

// viaEdge marks req as arriving on the given phone-leg transport at the edge
// proxy (Kamailio sets X-Hello-Transport from its received protocol).
func viaEdge(req *sip.Request, transport string) *sip.Request {
	req.AppendHeader(sip.NewHeader(transportHeader, transport))
	return req
}

func trustLoopback(c *Config, _ *Deps) {
	c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
}

// registerOver registers p through the (trusted) edge on transport.
func registerOver(t *testing.T, p *phone, transport string) {
	t.Helper()
	req := viaEdge(p.registerReq(300), transport)
	if res := p.authDo(req, AlgSHA256, p.pass); res.StatusCode != 200 {
		t.Fatalf("%s REGISTER over %s = %d", p.user, transport, res.StatusCode)
	}
}

func bindingTransport(t *testing.T, pbx *testPBX, p *phone) string {
	t.Helper()
	b := bindings(t, pbx, "sip:"+p.user+"@"+testDomain)["sip:"+p.user+"@"+p.addr]
	return b.Transport
}

// TestSIPTransports fails if a REGISTER's phone-leg transport is not
// recorded from the trusted edge (and only from it), if a sips: request is
// routed over a non-TLS hop (a caller not on TLS, a callee registered over
// UDP or TCP, a trunk or an external URI), or if a sips: call to a callee
// registered over TLS does not ring only the TLS devices.
func TestSIPTransports(t *testing.T) {
	t.Run("register records the edge transport", func(t *testing.T) {
		pbx := startPBX(t, ringAllDevices(), trustLoopback)
		for _, tc := range []struct{ header, want string }{{"tls", "tls"}, {"tcp", "tcp"}, {"udp", "udp"}, {"", "udp"}, {"bogus", "udp"}} {
			p := newPhone(t, pbx, "b1", "pb1")
			req := p.registerReq(300)
			if tc.header != "" {
				viaEdge(req, tc.header)
			}
			if res := p.authDo(req, AlgSHA256, "pb1"); res.StatusCode != 200 {
				t.Fatalf("REGISTER = %d", res.StatusCode)
			}
			if got := bindingTransport(t, pbx, p); got != tc.want {
				t.Errorf("X-Hello-Transport %q: binding transport = %q, want %q", tc.header, got, tc.want)
			}
		}
	})

	t.Run("an untrusted source cannot claim TLS", func(t *testing.T) {
		pbx := startPBX(t, ringAllDevices()) // no trusted proxy
		p := newPhone(t, pbx, "b1", "pb1")
		registerOver(t, p, "tls")
		if got := bindingTransport(t, pbx, p); got != "udp" {
			t.Fatalf("binding transport from an untrusted source = %q, want udp", got)
		}
		// And its sips: INVITE is refused as not on TLS.
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		req := a.inviteReq("200")
		req.Recipient.Scheme = "sips"
		viaEdge(req, "tls")
		if _, err := a.callReq(t.Context(), req); responseCode(err) != 480 {
			t.Fatalf("sips INVITE claiming TLS from an untrusted source: %v, want 480", err)
		}
	})

	t.Run("sips from a caller not on TLS is refused", func(t *testing.T) {
		pbx := startPBX(t, ringAllDevices(), trustLoopback)
		a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		registerOver(t, a, "udp")
		registerOver(t, b, "tls")
		req := viaEdge(a.inviteReq("200"), "udp")
		req.Recipient.Scheme = "sips"
		if _, err := a.callReq(t.Context(), req); responseCode(err) != 480 {
			t.Fatalf("sips INVITE over UDP: %v, want 480", err)
		}
		noReq(t, b.invites, 300*time.Millisecond, "INVITE to the callee of a refused sips call")
		if n := pbx.metric(t, "hello_sip_sips_refused_total", map[string]string{"reason": secureCallerNotTLS}); n != 1 {
			t.Fatalf("refusals (caller_not_tls) = %v, want 1", n)
		}
	})

	t.Run("sips to a callee with no TLS registration is refused", func(t *testing.T) {
		pbx := startPBX(t, ringAllDevices(), trustLoopback)
		a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		registerOver(t, a, "tls")
		registerOver(t, b, "tcp")
		req := viaEdge(a.inviteReq("200"), "tls")
		req.Recipient.Scheme = "sips"
		if _, err := a.callReq(t.Context(), req); responseCode(err) != 480 {
			t.Fatalf("sips INVITE to a TCP-registered callee: %v, want 480", err)
		}
		noReq(t, b.invites, 300*time.Millisecond, "INVITE over a non-TLS binding")
		if n := pbx.metric(t, "hello_sip_sips_refused_total", map[string]string{"reason": secureNoTLSBinding}); n != 1 {
			t.Fatalf("refusals (no_tls_binding) = %v, want 1", n)
		}
	})

	t.Run("sips rings only the TLS devices", func(t *testing.T) {
		pbx := startPBX(t, ringAllDevices(), trustLoopback)
		a, b1, b2 := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b2", "pb2")
		registerOver(t, a, "tls")
		registerOver(t, b1, "tls")
		registerOver(t, b2, "udp")
		req := viaEdge(a.inviteReq("200"), "tls")
		req.Recipient.Scheme = "sips"
		ch := make(chan callResult, 1)
		go func() {
			dcs, err := a.callReq(t.Context(), req)
			ch <- callResult{dcs, err}
		}()
		waitReq(t, b1.invites, "INVITE at the TLS device")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("sips call: %v", r.err)
		}
		noReq(t, b2.invites, 300*time.Millisecond, "INVITE at the UDP device of a sips call")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("sips never leaves for a trunk", func(t *testing.T) {
		r := &fakeRouter{extensions: map[string]bool{"100": true}}
		r.decide = func(routing.Call, routing.TrunkUsability) routing.Decision {
			return routing.Decision{Kind: routing.KindOutbound, Number: "+97140000100"}
		}
		pbx := startPBX(t, callerDevices(), withRouter(r, nil), trustLoopback)
		a := newPhone(t, pbx, "a1", "pa")
		registerOver(t, a, "tls")
		req := viaEdge(a.inviteReq("0097140000100"), "tls")
		req.Recipient.Scheme = "sips"
		if _, err := a.callReq(t.Context(), req); responseCode(err) != 480 {
			t.Fatalf("sips INVITE routed to a trunk: %v, want 480", err)
		}
		if n := pbx.metric(t, "hello_sip_sips_refused_total", map[string]string{"reason": secureNoTLSRoute}); n != 1 {
			t.Fatalf("refusals (no_tls_route) = %v, want 1", n)
		}
	})

	t.Run("a sip: call from a TLS phone still reaches a UDP phone", func(t *testing.T) {
		pbx := startPBX(t, ringAllDevices(), trustLoopback)
		a, b1 := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
		registerOver(t, a, "tls")
		registerOver(t, b1, "udp")
		req := viaEdge(a.inviteReq("200"), "tls")
		ch := make(chan callResult, 1)
		go func() {
			dcs, err := a.callReq(t.Context(), req)
			ch <- callResult{dcs, err}
		}()
		waitReq(t, b1.invites, "INVITE at the UDP device")
		r := waitCall(t, ch)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})
}

// TestRegistrationsByTransport fails if the per-transport registration
// gauge does not follow the transport phones registered over.
func TestRegistrationsByTransport(t *testing.T) {
	pbx := startPBX(t, ringAllDevices(), trustLoopback)
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	registerOver(t, a, "tls")
	registerOver(t, b, "udp")
	eventually(t, "tls=1 udp=1", func() bool {
		return pbx.metric(t, "hello_sip_registrations_by_transport", map[string]string{"transport": "tls"}) == 1 &&
			pbx.metric(t, "hello_sip_registrations_by_transport", map[string]string{"transport": "udp"}) == 1
	})
}
