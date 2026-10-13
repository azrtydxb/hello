// Command fakecarrier is a scriptable SIP carrier for Hello's lab tests: a
// registrar with digest auth, an OPTIONS responder, and a call endpoint
// whose outcome is chosen by the dialled number's last two digits. An HTTP
// control API places inbound calls to Hello and exposes what it received.
// It is test tooling, not product code.
//
// Outcomes by the last two digits of the dialled number:
//
//	86  486 Busy Here
//	03  503 Service Unavailable
//	08  100 Trying, then nothing (no final answer until CANCEL)
//	80  180 Ringing forever (until CANCEL)
//	*   180 Ringing, then 200 OK with SDP
//
// POST /mode {"failWith":code} overrides every outcome with code.
//
// POST /call also accepts "rtp": true. The call then carries real RTP: the
// INVITE's offer names the call target's own address (the SIP node that will
// anchor the media, resolvable from the carrier's network) on a UDP socket
// the carrier binds, and once the call is answered the carrier sends G.711
// silence from that socket to the answer's media address and counts what
// comes back. The response then reports "rtpSent" and "rtpReceived". This is
// how the lab proves audio flows both ways on an anchored call; without it
// the carrier is SIP-only and answers with an unroutable TEST-NET address.
// The INVITE's Request-URI is "to"@"FAKE_HELLO_DOMAIN", user verbatim — any
// user part, so a lab can address an internal extension number or any DID.
//
// Environment: FAKE_NAME, FAKE_SIP (listen, default 0.0.0.0:5060),
// FAKE_HTTP (default :8090), FAKE_USER and FAKE_PASSWORD (enable REGISTER
// digest), FAKE_REALM (default carrier.test), FAKE_INVITE_AUTH ("", "401" or
// "407": challenge INVITEs), FAKE_HELLO_DOMAIN (Request-URI host for inbound
// calls, default hello.lab).
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/test/sipua"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

var sdpAnswer = []byte("v=0\r\no=carrier 1 1 IN IP4 192.0.2.99\r\ns=-\r\nc=IN IP4 192.0.2.99\r\nt=0 0\r\nm=audio 50000 RTP/AVP 8 0\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:0 PCMU/8000\r\n")

// Request is one SIP request the carrier received, for test inspection.
type Request struct {
	Method     string            `json:"method"`
	RequestURI string            `json:"requestUri"`
	From       string            `json:"from"`     // From URI user part
	FromName   string            `json:"fromName"` // display name
	To         string            `json:"to"`
	Source     string            `json:"source"`
	Headers    map[string]string `json:"headers"` // selected headers
	Outcome    int               `json:"outcome,omitempty"`
	At         time.Time         `json:"at"`
}

type carrier struct {
	name, user, password, realm, inviteAuth, helloDomain string
	log                                                  *slog.Logger

	mu       sync.Mutex
	failWith int // when non-zero, every INVITE is answered with this code
	requests []Request
	regs     map[string]time.Time // contact -> expiry
	nonces   map[string]bool
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	c := &carrier{
		name: env("FAKE_NAME", "carrier"), user: os.Getenv("FAKE_USER"), password: os.Getenv("FAKE_PASSWORD"),
		realm: env("FAKE_REALM", "carrier.test"), inviteAuth: os.Getenv("FAKE_INVITE_AUTH"),
		helloDomain: env("FAKE_HELLO_DOMAIN", "hello.lab"),
		regs:        map[string]time.Time{}, nonces: map[string]bool{},
	}
	c.log = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("carrier", c.name)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := c.run(ctx, env("FAKE_SIP", "0.0.0.0:5060"), env("FAKE_HTTP", ":8090")); err != nil && !errors.Is(err, context.Canceled) {
		c.log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func (c *carrier) run(ctx context.Context, sipAddr, httpAddr string) error {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("fakecarrier/" + c.name))
	if err != nil {
		return err
	}
	defer func() { _ = ua.Close() }()
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return err
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientNAT())
	if err != nil {
		return err
	}
	dlg := sipgo.NewDialogServerCache(client, sip.ContactHeader{Address: sip.Uri{User: c.name, Host: hostOf(sipAddr), Port: portOf(sipAddr)}})

	srv.OnRegister(c.onRegister)
	srv.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		c.record(req, 200)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) { c.onInvite(dlg, req, tx) })
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = dlg.ReadAck(req, tx) })
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		c.record(req, 200)
		if err := dlg.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		}
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /log", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		writeJSON(w, c.requests)
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		c.requests = nil
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /registrations", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		out := []string{}
		for contact, exp := range c.regs {
			if time.Now().Before(exp) {
				out = append(out, contact)
			}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /call", c.placeCall)
	// POST /mode {"failWith":503} answers every INVITE with that code;
	// {"failWith":0} restores the scripted outcomes.
	mux.HandleFunc("POST /mode", func(w http.ResponseWriter, r *http.Request) {
		var m struct{ FailWith int }
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil || (m.FailWith != 0 && (m.FailWith < 400 || m.FailWith > 699)) {
			http.Error(w, "want {failWith: 0 or 400-699}", http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.failWith = m.FailWith
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	hs := &http.Server{Addr: httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = hs.Close() }()
	go func() {
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("http", "error", err)
		}
	}()
	c.log.Info("listening", "sip", sipAddr, "http", httpAddr, "registrar_auth", c.user != "", "invite_auth", c.inviteAuth)
	return srv.ListenAndServe(ctx, "udp", sipAddr)
}

func (c *carrier) record(req *sip.Request, outcome int) {
	r := Request{Method: string(req.Method), RequestURI: req.Recipient.String(), Source: req.Source(), Outcome: outcome, At: time.Now(), Headers: map[string]string{}}
	if f := req.From(); f != nil {
		r.From, r.FromName = f.Address.User, f.DisplayName
	}
	if to := req.To(); to != nil {
		r.To = to.Address.User
	}
	for _, h := range []string{"P-Asserted-Identity", "Contact", "Expires", "Route", "User-Agent"} {
		if v := req.GetHeader(h); v != nil {
			r.Headers[h] = v.Value()
		}
	}
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.mu.Unlock()
}

// challenge answers with a fresh digest challenge in hdr (WWW- or Proxy-).
func (c *carrier) challenge(req *sip.Request, tx sip.ServerTransaction, code int, hdr string) {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	nonce := hex.EncodeToString(b)
	c.mu.Lock()
	c.nonces[nonce] = true
	c.mu.Unlock()
	reason := "Unauthorized"
	if code == sip.StatusProxyAuthRequired {
		reason = "Proxy Authentication Required"
	}
	res := sip.NewResponseFromRequest(req, code, reason, nil)
	res.AppendHeader(sip.NewHeader(hdr, fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5, qop="auth"`, c.realm, nonce)))
	_ = tx.Respond(res)
}

// authorized checks a digest response in hdr against the carrier's
// credentials and a nonce it issued.
func (c *carrier) authorized(req *sip.Request, hdr string) bool {
	h := req.GetHeader(hdr)
	if h == nil {
		return false
	}
	cred, err := digest.ParseCredentials(h.Value())
	if err != nil || cred.Username != c.user {
		return false
	}
	c.mu.Lock()
	known := c.nonces[cred.Nonce]
	c.mu.Unlock()
	if !known {
		return false
	}
	want, err := digest.Digest(&digest.Challenge{Realm: cred.Realm, Nonce: cred.Nonce, QOP: []string{cred.QOP}, Algorithm: cred.Algorithm},
		digest.Options{Method: string(req.Method), URI: cred.URI, Username: c.user, Password: c.password, Count: cred.Nc, Cnonce: cred.Cnonce})
	return err == nil && want.Response == cred.Response
}

func (c *carrier) onRegister(req *sip.Request, tx sip.ServerTransaction) {
	if c.user != "" && !c.authorized(req, "Authorization") {
		c.record(req, 401)
		c.challenge(req, tx, sip.StatusUnauthorized, "WWW-Authenticate")
		return
	}
	exp := 3600
	if e := req.GetHeader("Expires"); e != nil {
		_, _ = fmt.Sscanf(e.Value(), "%d", &exp)
	}
	c.mu.Lock()
	for _, h := range req.GetHeaders("Contact") {
		contact := strings.Trim(strings.SplitN(h.Value(), ";", 2)[0], "<> ")
		if exp == 0 {
			delete(c.regs, contact)
		} else {
			c.regs[contact] = time.Now().Add(time.Duration(exp) * time.Second)
		}
	}
	c.mu.Unlock()
	c.record(req, 200)
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	if ct := req.GetHeader("Contact"); ct != nil {
		res.AppendHeader(sip.NewHeader("Contact", ct.Value()))
	}
	res.AppendHeader(sip.NewHeader("Expires", fmt.Sprint(exp)))
	_ = tx.Respond(res)
}

func (c *carrier) onInvite(dlg *sipgo.DialogServerCache, req *sip.Request, tx sip.ServerTransaction) {
	switch c.inviteAuth {
	case "401":
		if !c.authorized(req, "Authorization") {
			c.record(req, 401)
			c.challenge(req, tx, sip.StatusUnauthorized, "WWW-Authenticate")
			return
		}
	case "407":
		if !c.authorized(req, "Proxy-Authorization") {
			c.record(req, 407)
			c.challenge(req, tx, sip.StatusProxyAuthRequired, "Proxy-Authenticate")
			return
		}
	}
	number := req.Recipient.User
	suffix := number
	if len(number) >= 2 {
		suffix = number[len(number)-2:]
	}
	sess, err := dlg.ReadInvite(req, tx)
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		return
	}
	canceled := make(chan struct{})
	var once sync.Once
	tx.OnCancel(func(*sip.Request) { once.Do(func() { close(canceled) }) })
	_ = sess.Respond(sip.StatusTrying, "Trying", nil)
	c.mu.Lock()
	failWith := c.failWith
	c.mu.Unlock()
	if failWith != 0 {
		c.record(req, failWith)
		_ = sess.Respond(failWith, "Forced Failure", nil)
		return
	}
	switch suffix {
	case "86":
		c.record(req, 486)
		_ = sess.Respond(sip.StatusBusyHere, "Busy Here", nil)
		return
	case "03":
		c.record(req, 503)
		_ = sess.Respond(sip.StatusServiceUnavailable, "Service Unavailable", nil)
		return
	case "08", "80":
		if suffix == "80" {
			_ = sess.Respond(sip.StatusRinging, "Ringing", nil)
		}
		c.record(req, 0)
		// Hold the transaction (sipgo ends it when this handler returns).
		select {
		case <-canceled:
		case <-tx.Done():
		case <-time.After(3 * time.Minute):
		}
		return
	}
	_ = sess.Respond(sip.StatusRinging, "Ringing", nil)
	c.record(req, 200)
	_ = sess.RespondSDP(sdpAnswer)
}

// placeCall places an inbound call to Hello as the carrier would:
// {"from":"+971...","to":"<DID>","target":"hello-sip-1:5060","hangupAfterMs":1000}
// and answers {"status":<final code>}. With "rtp": true the call carries
// real audio both ways and the answer also reports "rtpSent" and
// "rtpReceived" (see the package comment).
func (c *carrier) placeCall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From, To, Target string
		HangupAfterMs    int
		RTP              bool
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.To == "" || in.Target == "" {
		http.Error(w, "want {from,to,target}", http.StatusBadRequest)
		return
	}
	var rt *rtpSession
	offer := sdpAnswer
	if in.RTP {
		// The offer names the target host's own address: that is the SIP
		// node (or, in tests, the peer) whose network the answer's media
		// address lives in, so it is an address the carrier can send RTP
		// to. A name resolves here, inside the carrier's network.
		addr, err := targetMediaAddr(r.Context(), in.Target)
		if err != nil {
			http.Error(w, "media address: "+err.Error(), http.StatusBadRequest)
			return
		}
		if rt, err = newRTPSession(r.Context(), addr); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rt.close()
		offer = rt.offer
		// RTP needs an answered call to flow on; the default gives it a
		// window even without an explicit hangupAfterMs.
		if in.HangupAfterMs <= 0 {
			in.HangupAfterMs = 1000
		}
	}
	p, err := sipua.New(sipua.Options{User: in.From, Domain: c.helloDomain, Proxy: in.Target, Listen: "0.0.0.0:0", ContactHost: os.Getenv("FAKE_CONTACT_HOST")})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := p.Dial(ctx, in.To, offer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp := map[string]any{"status": out.Status}
	if out.Status == 200 {
		if rt != nil {
			rt.run(out.Response.Body())
		}
		if in.HangupAfterMs > 0 {
			time.Sleep(time.Duration(in.HangupAfterMs) * time.Millisecond)
		}
		if rt != nil {
			sent, got := rt.stop()
			resp["rtpSent"], resp["rtpReceived"] = sent, got
		}
		_ = out.Hangup(ctx)
	}
	writeJSON(w, resp)
}

// targetMediaAddr resolves a call target's host to the IPv4 address the
// carrier advertises as its own in the SDP offer: the peer's own network,
// so the answer's media address (the same host when the media anchors) is
// one the carrier can reach.
func targetMediaAddr(ctx context.Context, target string) (string, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
		return ip.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ia := range ips {
		if v4 := ia.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("no IPv4 address for %s", host)
}

// rtpSession is one placed call's media: a bound UDP socket the offer names,
// sending G.711 silence (0x55, PCMA's silence byte — the marker a peer can
// recognise) to the answer's media address and counting the RTP that comes
// back from any other SSRC.
type rtpSession struct {
	conn *net.UDPConn
	// offer is the SDP offer naming this session's address and port.
	offer []byte

	ssrc uint32
	pt   uint8
	seq  uint16
	ts   uint32

	mu       sync.Mutex
	sent     int
	received int
	// stopCh ends run's goroutines; nil until run.
	stopCh chan struct{}
}

func newRTPSession(ctx context.Context, addr string) (*rtpSession, error) {
	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("not a UDP socket")
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	var b [4]byte
	_, _ = rand.Read(b[:])
	rt := &rtpSession{conn: udp, ssrc: binary.BigEndian.Uint32(b[:]), pt: 8}
	rt.offer = media.BuildAudioSDP(addr, port, rt.pt, 0, 0)
	return rt, nil
}

// run starts sending to the answered call's media address and counting what
// arrives; stop ends both and reports the counters. It is called once.
func (rt *rtpSession) run(answer []byte) {
	ans, err := media.ParseAudioSDP(answer)
	if err != nil {
		return // no media address to aim at; nothing flows
	}
	peer := &net.UDPAddr{IP: net.ParseIP(ans.Address), Port: ans.Port}
	stop := make(chan struct{})
	go func() { // sender: one 20ms frame of PCMA silence at a time
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		payload := bytes.Repeat([]byte{0x55}, 160)
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				rt.seq++
				rt.ts += 160
				pkt := media.BuildRTP(rt.pt, rt.seq, rt.ts, rt.ssrc, false, payload)
				if _, err := rt.conn.WriteToUDP(pkt, peer); err == nil {
					rt.mu.Lock()
					rt.sent++
					rt.mu.Unlock()
				}
			}
		}
	}()
	go func() { // receiver: count RTP from any other SSRC
		buf := make([]byte, 1500)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = rt.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, _, err := rt.conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if pkt, ok := media.ParseRTP(buf[:n]); ok && pkt.SSRC != rt.ssrc {
				rt.mu.Lock()
				rt.received++
				rt.mu.Unlock()
			}
		}
	}()
	rt.stopCh = stop
}

// stop ends run's exchange and reports the counters. It is called once.
func (rt *rtpSession) stop() (sent, received int) {
	if rt.stopCh != nil {
		close(rt.stopCh)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sent, rt.received
}

// close releases the session's socket.
func (rt *rtpSession) close() { _ = rt.conn.Close() }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func hostOf(addr string) string {
	h, _, _ := net.SplitHostPort(addr)
	if ip := net.ParseIP(h); h == "" || (ip != nil && ip.IsUnspecified()) {
		if name, err := os.Hostname(); err == nil {
			return name
		}
	}
	return h
}

func portOf(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	var n int
	_, _ = fmt.Sscanf(p, "%d", &n)
	return n
}
