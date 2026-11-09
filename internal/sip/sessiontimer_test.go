package sip

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// grandstreamUA is what a WP822 sends; Hello asks a phone of a known vendor
// for session timers (prov.SupportsSessionTimers).
const grandstreamUA = "Grandstream WP822 1.0.11.78"

// withSessions sets the session timer intervals and the dead-media window.
func withSessions(expires, minSE, dead time.Duration) pbxOpt {
	return func(c *Config, _ *Deps) {
		c.SessionExpires, c.SessionMinSE, c.MediaDeadAfter = expires, minSE, dead
	}
}

// logCapture keeps the records at warning level or above.
type logCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (l *logCapture) Enabled(_ context.Context, lv slog.Level) bool { return lv >= slog.LevelWarn }
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	l.recs = append(l.recs, r)
	l.mu.Unlock()
	return nil
}
func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(string) slog.Handler      { return l }
func (l *logCapture) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.recs {
		out = append(out, r.Level.String()+" "+r.Message)
	}
	return out
}

// registerAs registers p with a vendor User-Agent.
func registerAs(t *testing.T, p *phone, ua string) {
	t.Helper()
	req := p.registerReq(300)
	req.AppendHeader(sip.NewHeader("User-Agent", ua))
	if res := p.authDo(req, AlgSHA256, p.pass); res.StatusCode != 200 {
		t.Fatalf("%s register = %d", p.user, res.StatusCode)
	}
}

// answerWithTimer answers like answerAfter(nil), naming the session timer
// the phone wants (the phone's own refresher choice).
func answerWithTimer(sessionExpires string) calleeFn {
	return func(p *phone, _ *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(200, "OK", []byte(p.sdp),
			sip.NewHeader("Content-Type", "application/sdp"),
			sip.NewHeader("Session-Expires", sessionExpires),
			sip.NewHeader("Require", "timer"))
	}
}

func sessionPair(t *testing.T, opts ...pbxOpt) (*testPBX, *phone, *phone) {
	t.Helper()
	pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1")}, opts...)
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	registerAs(t, b, grandstreamUA)
	return pbx, a, b
}

func headerOfReq(r *sip.Request, name string) string {
	if h := r.GetHeader(name); h != nil {
		return h.Value()
	}
	return ""
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestSessionTimersAndKeepalive covers phone-feature-parity S-9: CRLF
// keep-alives, RFC 4028 session timers (negotiation, refreshing, teardown on a
// failed or missing refresh), dead-media teardown, the anchor re-learning a
// new media address, and a re-REGISTER from a new address replacing the old
// contact.
func TestSessionTimersAndKeepalive(t *testing.T) {
	t.Run("crlf ping over UDP is ignored without an error log", func(t *testing.T) {
		logs := &logCapture{}
		pbx := startPBX(t, callerDevices(), func(_ *Config, d *Deps) { d.Log = slog.New(logs) })
		conn, err := net.Dial("udp", pbx.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		for _, ping := range []string{"\r\n\r\n", "\r\n", "\r\n\r\n"} {
			if _, err := conn.Write([]byte(ping)); err != nil {
				t.Fatal(err)
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		if n, err := conn.Read(make([]byte, 64)); err == nil {
			t.Fatalf("a CRLF ping over UDP was answered with %d bytes", n)
		}
		if msgs := logs.messages(); len(msgs) > 0 {
			t.Fatalf("a CRLF ping logged: %v", msgs)
		}
		// The listener is still healthy.
		p := newPhone(t, pbx, "a1", "pa")
		p.register(t)
	})

	t.Run("crlf ping over TCP gets one CRLF back", func(t *testing.T) {
		// hello-sip's UDP socket is the only listener of Serve; the TCP and
		// TLS listeners (S-13, S-14) use sipgo's transport, whose keep-alive
		// handling this pins: a double CRLF is answered with a single CRLF,
		// a single CRLF is not answered, and neither logs an error.
		logs := &logCapture{}
		ua, err := sipgo.NewUA(sipgo.WithUserAgentTransportLayerOptions(sip.WithTransportLayerLogger(slog.New(logs))))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ua.Close() }()
		srv, err := sipgo.NewServer(ua, sipgo.WithServerLogger(slog.New(logs)))
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = srv.ServeTCP(ln) }()
		go func() { <-ctx.Done(); _ = ln.Close() }()
		var conn net.Conn
		eventually(t, "tcp listener", func() bool {
			conn, err = net.Dial("tcp", ln.Addr().String())
			return err == nil
		})
		defer func() { _ = conn.Close() }()
		if _, err := conn.Write([]byte("\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		if err != nil || string(buf[:n]) != "\r\n" {
			t.Fatalf("pong = %q, %v; want one CRLF", buf[:n], err)
		}
		if _, err := conn.Write([]byte("\r\n")); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if n, err := conn.Read(buf); err == nil {
			t.Fatalf("a single CRLF was answered with %q", buf[:n])
		}
		if msgs := logs.messages(); len(msgs) > 0 {
			t.Fatalf("keep-alives logged: %v", msgs)
		}
	})

	t.Run("a failed refresh tears the call down", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false), withSessions(2*time.Second, time.Second, 0))
		b.setCallee(answerAfter(nil))
		dcs, err := a.call(callCtx(t), "200")
		if err != nil {
			t.Fatal(err)
		}
		// Hello is the refresher towards the caller, which asked for no timer.
		if got := dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uas" {
			t.Fatalf("200 to the caller: Session-Expires = %v, want 2;refresher=uas", got)
		}
		inv := waitReq(t, b.invites, "INVITE to the callee")
		if got := headerOfReq(inv, "Session-Expires"); got != "2;refresher=uac" {
			t.Fatalf("INVITE Session-Expires = %q, want 2;refresher=uac", got)
		}
		if !strings.Contains(headerOfReq(inv, "Supported"), "timer") || headerOfReq(inv, "Min-SE") != "1" {
			t.Fatalf("INVITE lacks Supported: timer / Min-SE: 1")
		}
		// Both dialogs get refreshed, and the call survives that.
		for name, ch := range map[string]chan *sip.Request{"callee": b.reinvites, "caller": a.reinvites} {
			r := waitReq(t, ch, "refresh of the "+name)
			if got := headerOfReq(r, "Session-Expires"); got != "2;refresher=uac" {
				t.Fatalf("%s refresh Session-Expires = %q", name, got)
			}
		}
		time.Sleep(300 * time.Millisecond)
		if n := len(liveCalls(t, pbx)); n != 1 {
			t.Fatalf("live calls after a successful refresh = %d, want 1", n)
		}
		// The callee's phone is gone: the next refresh fails and the call ends.
		b.mu.Lock()
		b.reinviteStatus = 481
		b.mu.Unlock()
		waitReq(t, a.byes, "BYE to the caller")
		waitReq(t, b.byes, "BYE to the callee")
		cd := pbx.nextCDR(t)
		if cd.TerminationSide != cdr.SideSystem || !strings.Contains(cd.FailureReason, "session timer") {
			t.Fatalf("CDR = side %q reason %q, want a system teardown by the session timer", cd.TerminationSide, cd.FailureReason)
		}
		eventually(t, "no live calls", func() bool { return len(liveCalls(t, pbx)) == 0 })
		if v := pbx.metric(t, "hello_sip_session_teardowns_total", map[string]string{"reason": "refresh_failed"}); v != 1 {
			t.Fatalf("teardown metric = %v, want 1", v)
		}
	})

	t.Run("a refresh nobody answers tears the call down", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false), withSessions(2*time.Second, time.Second, 0),
			func(c *Config, _ *Deps) { c.SessionRefreshTimeout = time.Second })
		b.mu.Lock()
		b.reinviteStatus = -1 // the handset left the Wi-Fi: no answer at all
		b.mu.Unlock()
		if _, err := a.call(callCtx(t), "200"); err != nil {
			t.Fatal(err)
		}
		waitReq(t, b.reinvites, "refresh of the callee")
		waitReq(t, a.byes, "BYE to the caller") // within the refresh timeout
		cd := pbx.nextCDR(t)
		if cd.TerminationSide != cdr.SideSystem || !strings.Contains(cd.FailureReason, "session timer") {
			t.Fatalf("CDR = side %q reason %q", cd.TerminationSide, cd.FailureReason)
		}
	})

	t.Run("a phone that is the refresher must refresh", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false), withSessions(2*time.Second, time.Second, 0))
		b.setCallee(answerWithTimer("2;refresher=uas"))
		if _, err := a.call(callCtx(t), "200"); err != nil {
			t.Fatal(err)
		}
		// b1 named itself the refresher and never refreshes: Hello does not
		// refresh b1, and ends the call before the interval runs out.
		started := time.Now()
		waitReq(t, b.byes, "BYE to the silent refresher")
		waitReq(t, a.byes, "BYE to the caller")
		if d := time.Since(started); d > 2500*time.Millisecond {
			t.Fatalf("teardown took %v, want before the 2 s interval ended", d)
		}
		for _, r := range drain(b.reinvites) {
			t.Fatalf("Hello refreshed a leg whose phone is the refresher: %s", r.StartLine())
		}
		cd := pbx.nextCDR(t)
		if !strings.Contains(cd.FailureReason, "refresh missing") {
			t.Fatalf("reason = %q, want refresh missing", cd.FailureReason)
		}
	})

	t.Run("a caller that refreshes keeps the call", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false), withSessions(2*time.Second, time.Second, 0))
		req := a.inviteReq("200")
		req.AppendHeader(sip.NewHeader("Supported", "timer"))
		req.AppendHeader(sip.NewHeader("Session-Expires", "2;refresher=uac"))
		dcs, err := a.callReq(callCtx(t), req)
		if err != nil {
			t.Fatal(err)
		}
		if got := dcs.InviteResponse.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uac" {
			t.Fatalf("200 Session-Expires = %v, want the caller's refresher=uac kept", got)
		}
		if got := dcs.InviteResponse.GetHeader("Require"); got == nil || !strings.Contains(got.Value(), "timer") {
			t.Fatalf("200 lacks Require: timer for a uac refresher")
		}
		// The caller refreshes five times over 3.5 s: longer than the 2 s
		// interval, so Hello's own waiting timer would have ended the call.
		target := *dcs.InviteRequest.Recipient.Clone()
		if ct := dcs.InviteResponse.Contact(); ct != nil {
			target = *ct.Address.Clone()
		}
		for i := 0; i < 5; i++ {
			time.Sleep(700 * time.Millisecond)
			up := sip.NewRequest(sip.UPDATE, target)
			up.AppendHeader(sip.NewHeader("Session-Expires", "2;refresher=uac"))
			up.SetTransport("UDP")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			res, err := dcs.Do(ctx, up)
			cancel()
			if err != nil || res.StatusCode != 200 {
				t.Fatalf("refresh %d: %v %v", i, res, err)
			}
			if got := res.GetHeader("Session-Expires"); got == nil || got.Value() != "2;refresher=uac" {
				t.Fatalf("refresh %d answer Session-Expires = %v", i, got)
			}
		}
		if n := len(liveCalls(t, pbx)); n != 1 {
			t.Fatalf("live calls = %d, want the call still up", n)
		}
		noReq(t, a.byes, 100*time.Millisecond, "BYE to a caller that refreshed")
		_ = b
		hangup(t, dcs)
	})

	t.Run("an interval below Min-SE is refused with 422", func(t *testing.T) {
		_, a, _ := sessionPair(t, withMedia(false), withSessions(4*time.Second, 2*time.Second, 0))
		req := a.inviteReq("200")
		req.AppendHeader(sip.NewHeader("Supported", "timer"))
		req.AppendHeader(sip.NewHeader("Session-Expires", "1"))
		res := a.do(req)
		if res.StatusCode != 422 {
			t.Fatalf("status = %d, want 422", res.StatusCode)
		}
		if got := res.GetHeader("Min-SE"); got == nil || got.Value() != "2" {
			t.Fatalf("Min-SE = %v, want 2", got)
		}
	})

	t.Run("session timers off leave the signalling alone", func(t *testing.T) {
		_, a, b := sessionPair(t, withMedia(false))
		dcs, err := a.call(callCtx(t), "200")
		if err != nil {
			t.Fatal(err)
		}
		if dcs.InviteResponse.GetHeader("Session-Expires") != nil {
			t.Fatal("a Session-Expires without session timers configured")
		}
		inv := waitReq(t, b.invites, "INVITE")
		if headerOfReq(inv, "Session-Expires") != "" || headerOfReq(inv, "Supported") != "" {
			t.Fatal("the INVITE asks for session timers while they are off")
		}
		hangup(t, dcs)
	})

	t.Run("a call with no RTP either way is ended, with RTP it is not", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false), withSessions(0, 0, 2*time.Second))
		dcs, err := a.call(callCtx(t), "200")
		if err != nil {
			t.Fatal(err)
		}
		port := anchoredSDP(t, pbx, dcs.InviteResponse.Body()).Port
		stop := sendRTP(t, port, 150*time.Millisecond)
		// 4.5 s is more than twice the window: only the stream keeps it up.
		noReq(t, b.byes, 4500*time.Millisecond, "BYE while media flows")
		stop()
		waitReq(t, b.byes, "BYE after the media stopped")
		waitReq(t, a.byes, "BYE to the caller")
		cd := pbx.nextCDR(t)
		if cd.TerminationSide != cdr.SideSystem || !strings.Contains(cd.FailureReason, "no media") {
			t.Fatalf("CDR = side %q reason %q, want a system teardown for no media", cd.TerminationSide, cd.FailureReason)
		}
		if v := pbx.metric(t, "hello_sip_session_teardowns_total", map[string]string{"reason": "media_dead"}); v != 1 {
			t.Fatalf("teardown metric = %v, want 1", v)
		}
	})

	t.Run("a held call is not ended for silence", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false), withSessions(0, 0, 2*time.Second))
		dcs, err := a.call(callCtx(t), "200")
		if err != nil {
			t.Fatal(err)
		}
		if res := a.reinviteAsCaller(t, dcs, a.sdp+"a=sendonly\r\n"); res.StatusCode != 200 {
			t.Fatalf("hold = %d", res.StatusCode)
		}
		noReq(t, b.byes, 4500*time.Millisecond, "BYE of a held call")
		if n := len(liveCalls(t, pbx)); n != 1 {
			t.Fatalf("live calls = %d, want the held call up", n)
		}
		// Resumed and silent, the call is ended after a fresh window.
		if res := a.reinviteAsCaller(t, dcs, a.sdp); res.StatusCode != 200 {
			t.Fatalf("resume = %d", res.StatusCode)
		}
		waitReq(t, b.byes, "BYE after the resumed call went silent")
	})

	t.Run("a re-INVITE with a new media address is relayed and relearned", func(t *testing.T) {
		pbx, a, b := sessionPair(t, withMedia(false))
		dcs, err := a.call(callCtx(t), "200")
		if err != nil {
			t.Fatal(err)
		}
		ref, ok := pbx.srv.lookup(dcs.InviteRequest.CallID().Value())
		if !ok {
			t.Fatal("call not found")
		}
		relay := ref.c.anchorRelay()
		if relay == nil {
			t.Fatal("call is not anchored")
		}
		old := relay.LegTarget(legCaller)
		// The handset roamed: same call, a new IP and port in the SDP.
		roamed := strings.NewReplacer("IN IP4 127.0.0.1", "IN IP4 127.0.0.2",
			"m=audio "+strconv.Itoa(a.mediaPort()), "m=audio 45678").Replace(a.sdp)
		if res := a.reinviteAsCaller(t, dcs, roamed); res.StatusCode != 200 {
			t.Fatalf("roam re-INVITE = %d", res.StatusCode)
		}
		waitReq(t, b.reinvites, "the re-INVITE relayed to the callee")
		got := relay.LegTarget(legCaller)
		if got == nil || got.String() != "127.0.0.2:45678" {
			t.Fatalf("caller leg target = %v (was %v), want 127.0.0.2:45678", got, old)
		}
		if peer := relay.LegTarget(legCallee); peer == nil || peer.IP.String() == "127.0.0.2" {
			t.Fatalf("the callee leg moved with the caller's roam: %v", peer)
		}
		// An identical refresh does not disturb the leg.
		if res := a.reinviteAsCaller(t, dcs, roamed); res.StatusCode != 200 {
			t.Fatalf("second re-INVITE = %d", res.StatusCode)
		}
		hangup(t, dcs)
	})

	t.Run("a re-REGISTER from a new address replaces the old contact", func(t *testing.T) {
		pbx := startPBX(t, callerDevices())
		p := newPhone(t, pbx, "a1", "pa")
		aor := "sip:a1@" + testDomain
		reg := func(callID, instance, contact string) {
			t.Helper()
			c := "<" + contact + ">"
			if instance != "" {
				c += `;+sip.instance="<urn:uuid:` + instance + `>"`
			}
			req := p.registerReq(300, c)
			cid := sip.CallIDHeader(callID)
			req.AppendHeader(&cid)
			if res := p.authDo(req, AlgSHA256, p.pass); res.StatusCode != 200 {
				t.Fatalf("register = %d", res.StatusCode)
			}
		}
		const oldC, newC, other = "sip:a1@127.0.0.1:5101", "sip:a1@127.0.0.1:5102", "sip:a1@127.0.0.1:5103"
		reg("call-1", "aaaa", oldC)
		if got := bindings(t, pbx, aor); len(got) != 1 || got[oldC].Instance != "urn:uuid:aaaa" {
			t.Fatalf("bindings = %v", got)
		}
		reg("call-2", "aaaa", newC) // same instance, new address, new Call-ID
		if got := bindings(t, pbx, aor); len(got) != 1 || got[newC].Instance == "" {
			t.Fatalf("same instance: bindings = %v, want only %s", keysOf(got), newC)
		}
		reg("call-2", "", other) // no instance: the same Call-ID names the phone
		if got := bindings(t, pbx, aor); len(got) != 1 || got[other].CallID != "call-2" {
			t.Fatalf("same Call-ID: bindings = %v, want only %s", keysOf(got), other)
		}
		reg("call-3", "bbbb", oldC) // another instance and Call-ID: a second device
		if got := bindings(t, pbx, aor); len(got) != 2 {
			t.Fatalf("a different instance replaced a contact: %v", keysOf(got))
		}
		reg("call-3", "bbbb", oldC) // a plain refresh keeps both
		if got := bindings(t, pbx, aor); len(got) != 2 {
			t.Fatalf("a refresh changed the bindings: %v", keysOf(got))
		}
	})
}

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func drain(ch chan *sip.Request) []*sip.Request {
	var out []*sip.Request
	for {
		select {
		case r := <-ch:
			out = append(out, r)
		default:
			return out
		}
	}
}

// sendRTP streams PCMU packets at the anchor's relay port from 127.0.0.1
// every interval until the returned stop is called (or the test ends).
func sendRTP(t *testing.T, port int, every time.Duration) (stop func()) {
	t.Helper()
	conn, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(every)
		defer tk.Stop()
		var seq uint16
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				seq++
				pkt := make([]byte, 12+160)
				pkt[0], pkt[1] = 0x80, 0
				pkt[2], pkt[3] = byte(seq>>8), byte(seq)
				pkt[7] = byte(seq) // timestamp low bits
				pkt[11] = 1        // SSRC
				_, _ = conn.Write(pkt)
			}
		}
	}()
	stop = func() {
		once.Do(func() { close(done); wg.Wait(); _ = conn.Close() })
	}
	t.Cleanup(stop)
	return stop
}

// mediaPort is the RTP port the phone's SDP names.
func (p *phone) mediaPort() int {
	a, err := media.ParseAudioSDP([]byte(p.sdp))
	if err != nil {
		p.t.Fatal(err)
	}
	return a.Port
}
