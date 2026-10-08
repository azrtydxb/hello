// Package fakeagent is a minimal fake talking-agent (spec voice-agents): a
// SIP UA that answers the calls Hello routes to a voice agent with G.711,
// verifies the signed header set of S-15 and S-16 before anything else,
// polls the runtime API with a service account and reloads on a revision
// change within two seconds, and reports each finished call back. It is
// test tooling for Hello's lab and unit tests, not product code, and it
// plays the role talking-agent plays: unknown SIP user 404, untrusted call
// 403, busy 486.
package fakeagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/voice"
)

// Options configures the fake agent.
type Options struct {
	// Listen is the local UDP address for SIP ("" is 127.0.0.1:0).
	Listen string
	// ContactHost is the host advertised in Contact and the SDP answer, for
	// when the agent is reached through another address than Listen (the
	// lab's Hello reaches it on the Docker edge gateway).
	ContactHost string
	// Keys are the X-Hello-Auth keys the agent accepts (S-16); a call whose
	// signature, key id, timestamp or correlation does not verify is 403.
	Keys []voice.Key
	// Tenant is the X-Hello-Tenant the agent insists on ("" accepts any).
	Tenant string
	// Now is the agent's clock; nil is time.Now. The shared signature
	// vectors carry fixed timestamps, so their test pins the clock.
	Now func() time.Time

	// Base is Hello's API origin (for example http://localhost:8081). Empty
	// runs the agent without a runtime: it then answers any signed call.
	Base string
	// Token is a ready bearer credential for the runtime API. When it is
	// empty, ClientID and ClientSecret fetch one by client credentials
	// (scope voice-runtime) and refresh it before it expires.
	Token        string
	ClientID     string
	ClientSecret string
	// Version is the talking-agent version the acks report.
	Version string
}

// Call is one INVITE the agent accepted, with the signed header values.
type Call struct {
	Agent, SIPUser, Tenant, Correlation string
	Caller, Called, Origin              string
}

// Behavior decides how the agent answers a call: 0 answers with a tone, any
// other code is the final response (403 rejected, 404 unknown, 486 full).
type Behavior func(Call) int

// Report is one call report the agent posted (S-21).
type Report struct {
	CorrelationID string `json:"correlationId"`
	AgentName     string `json:"agentName"`
	Outcome       string `json:"outcome"`
	Summary       string `json:"summary,omitempty"`
	TokensIn      int    `json:"tokensIn"`
	TokensOut     int    `json:"tokensOut"`
}

// Agent is one fake talking-agent. Start it with Start; stop it with Close.
type Agent struct {
	opts Options
	now  func() time.Time
	ctx  context.Context
	stop context.CancelFunc

	ua      *sipgo.UserAgent
	client  *sipgo.Client
	server  *sipgo.Server
	dua     *sipgo.DialogUA
	contact sip.ContactHeader
	sipConn net.PacketConn
	rtpConn net.PacketConn
	rtpAddr *net.UDPAddr

	http *http.Client

	replay voice.ReplayGuard
	// srvDlg holds the server dialogs by Call-ID, so the ACK reaches the
	// session that waits for it.
	srvDlg map[string]*sipgo.DialogServerSession

	mu        sync.Mutex
	behaviors map[string]Behavior
	view      voice.View
	loaded    bool
	calls     []Call
	reports   []Report
	acks      []voice.View
	errs      []error
	bearer    string    // cached client-credentials token
	bearerExp time.Time // when it expires

	rtpIn    atomic.Int64
	lastDone atomic.Int64 // Unix nanos of the last reload
}

// Start starts the agent's SIP listener and, with a Base, its runtime poll.
func Start(opts Options) (*Agent, error) {
	if len(opts.Keys) == 0 {
		return nil, errors.New("fakeagent: no X-Hello-Auth keys configured")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:0"
	}
	if opts.Version == "" {
		opts.Version = "fakeagent/1"
	}
	ctx, stop := context.WithCancel(context.Background())

	sipConn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", opts.Listen)
	if err != nil {
		stop()
		return nil, fmt.Errorf("fakeagent: SIP listener: %w", err)
	}
	host := opts.ContactHost
	if host == "" {
		host, _, _ = net.SplitHostPort(sipConn.LocalAddr().String())
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
		}
	}
	rtpConn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", host+":0")
	if err != nil {
		_ = sipConn.Close()
		stop()
		return nil, fmt.Errorf("fakeagent: RTP listener: %w", err)
	}
	rtpAddr, err := net.ResolveUDPAddr("udp", rtpConn.LocalAddr().String())
	if err != nil {
		_ = sipConn.Close()
		_ = rtpConn.Close()
		stop()
		return nil, fmt.Errorf("fakeagent: RTP address: %w", err)
	}

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("fakeagent"))
	if err != nil {
		_ = sipConn.Close()
		_ = rtpConn.Close()
		stop()
		return nil, err
	}
	_, portStr, _ := net.SplitHostPort(sipConn.LocalAddr().String())
	sipPort, _ := strconv.Atoi(portStr)
	// Send from the listening socket with rport, as a UA behind NAT does:
	// Hello's nodes may answer an address the agent only reaches by source.
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(host),
		sipgo.WithClientPort(sipPort),
		sipgo.WithClientConnectionAddr(sipConn.LocalAddr().String()), sipgo.WithClientNAT())
	if err != nil {
		_ = ua.Close()
		_ = sipConn.Close()
		_ = rtpConn.Close()
		stop()
		return nil, err
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = ua.Close()
		_ = sipConn.Close()
		_ = rtpConn.Close()
		stop()
		return nil, err
	}

	port := sipPort
	a := &Agent{
		opts:      opts,
		now:       opts.Now,
		ctx:       ctx,
		stop:      stop,
		ua:        ua,
		client:    client,
		server:    server,
		contact:   sip.ContactHeader{Address: sip.Uri{User: "fakeagent", Host: host, Port: port}},
		sipConn:   sipConn,
		rtpConn:   rtpConn,
		rtpAddr:   rtpAddr,
		http:      &http.Client{Timeout: 30 * time.Second},
		behaviors: map[string]Behavior{},
		srvDlg:    map[string]*sipgo.DialogServerSession{},
	}
	a.dua = &sipgo.DialogUA{Client: client, ContactHDR: a.contact, RewriteContact: true}
	a.routes()
	go func() { _ = server.ServeUDP(sipConn) }()
	// ServeUDP registers the socket with the transport layer from its own
	// goroutine; wait for it, as test/sipua does, before the first response.
	tl := ua.TransportLayer()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if c, _ := tl.GetConnection("udp", sipConn.LocalAddr().String()); c != nil {
			break
		}
		if time.Now().After(deadline) {
			a.Close()
			return nil, errors.New("fakeagent: SIP listener did not start")
		}
		time.Sleep(time.Millisecond)
	}
	go a.readRTP(ctx)
	if opts.Base != "" {
		go a.poll(ctx)
	}
	return a, nil
}

// Close stops the agent.
func (a *Agent) Close() {
	a.stop()
	_ = a.server.Close()
	_ = a.client.Close()
	_ = a.ua.Close()
	_ = a.sipConn.Close()
	_ = a.rtpConn.Close()
}

// Addr is the SIP address the agent answers on.
func (a *Agent) Addr() string {
	return a.sipConn.LocalAddr().String()
}

// SetBehavior installs how the agent answers calls to one agent name (the
// X-Hello-Agent value); the empty name is every agent without its own.
func (a *Agent) SetBehavior(agent string, b Behavior) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b == nil {
		delete(a.behaviors, agent)
		return
	}
	a.behaviors[agent] = b
}

// View is the runtime view the agent last loaded (the zero View before the
// first successful read).
func (a *Agent) View() voice.View {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.view
}

// LoadedAt is when the agent last swapped its view; zero before the first.
func (a *Agent) LoadedAt() time.Time {
	return time.Unix(0, a.lastDone.Load())
}

// Calls lists the INVITEs the agent accepted.
func (a *Agent) Calls() []Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Call(nil), a.calls...)
}

// Reports lists the call reports the agent posted.
func (a *Agent) Reports() []Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Report(nil), a.reports...)
}

// Acks lists the revisions the agent acknowledged, in order.
func (a *Agent) Acks() []voice.View {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]voice.View(nil), a.acks...)
}

// Errors lists every runtime error the poll hit, for test failures that
// explain themselves.
func (a *Agent) Errors() []error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]error(nil), a.errs...)
}

// RTPReceived counts the RTP packets the agent received from Hello's relay.
func (a *Agent) RTPReceived() int64 { return a.rtpIn.Load() }

// routes installs the SIP handlers.
func (a *Agent) routes() {
	a.server.OnInvite(a.onInvite)
	a.server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		// Hand the ACK to the session that waits for it; a stray one gets
		// the not-found answer, as a UA answers an unknown dialog.
		a.mu.Lock()
		s := a.srvDlg[req.CallID().Value()]
		a.mu.Unlock()
		if s != nil {
			_ = s.ReadAck(req, tx)
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
	})
	a.server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		// Read the BYE on the dialog it ends, so the call's wait sees the
		// end and reports the call; a stray one is 481.
		a.mu.Lock()
		s := a.srvDlg[req.CallID().Value()]
		a.mu.Unlock()
		if s != nil && s.ReadBye(req, tx) == nil {
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
	})
	a.server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
}

// onInvite verifies the call, answers it with a tone or refuses it, and
// reports it when it ends.
func (a *Agent) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	call, code := a.check(req)
	if code != 0 {
		_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason(code), nil))
		return
	}
	sess, err := a.dua.ReadInvite(req, tx)
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		return
	}
	a.mu.Lock()
	a.srvDlg[req.CallID().Value()] = sess
	a.calls = append(a.calls, call)
	behavior := a.behaviors[call.Agent]
	if behavior == nil {
		behavior = a.behaviors[""]
	}
	a.mu.Unlock()

	canceled := make(chan struct{})
	tx.OnCancel(func(*sip.Request) { close(canceled) })

	code = sip.StatusOK // no behavior answers with a tone
	if behavior != nil {
		code = behavior(call)
	}
	if code != 0 && code != sip.StatusOK {
		_ = sess.Respond(code, reason(code), nil)
		// sipgo kills the server transaction when this handler returns.
		select {
		case <-canceled:
		case <-tx.Done():
		case <-a.ctx.Done():
		case <-time.After(time.Minute):
		}
		return
	}

	answer := a.answerSDP(req.Body())
	_ = sess.Respond(sip.StatusTrying, "Trying", nil)
	if err := sess.RespondSDP(answer); err != nil {
		return
	}
	done := ended(&sess.Dialog)
	off, _ := media.ParseAudioSDP(req.Body())
	go a.speak(a.ctx, off, done)
	// Hold the transaction until the dialog ends, as test/sipua must.
	select {
	case <-done:
	case <-canceled:
	case <-tx.Done():
	case <-a.ctx.Done():
	case <-time.After(3 * time.Minute):
	}
	a.report(call)
}

// check verifies the S-15 header set and the S-16 signature and returns the
// call, or the response code that refuses it: 403 for a missing or bad
// header set, an unknown key, a wrong signature, a stale timestamp or a
// replayed correlation id, 404 for a SIP user the runtime view does not
// name.
func (a *Agent) check(req *sip.Request) (Call, int) {
	c := Call{
		Agent:       hdr(req, voice.HeaderAgent),
		Tenant:      hdr(req, voice.HeaderTenant),
		Correlation: hdr(req, voice.HeaderCorrelation),
		Caller:      hdr(req, voice.HeaderCaller),
		Called:      hdr(req, voice.HeaderCalled),
		Origin:      hdr(req, voice.HeaderCallerOrigin),
		SIPUser:     req.Recipient.User,
	}
	auth := hdr(req, voice.HeaderAuth)
	tsH := hdr(req, voice.HeaderTs)
	if c.Agent == "" || c.Tenant == "" || c.Correlation == "" || c.Caller == "" ||
		c.Called == "" || c.Origin == "" || auth == "" || tsH == "" || c.SIPUser == "" {
		return c, sip.StatusForbidden
	}
	ts, err := voice.ParseTs(tsH)
	if err != nil {
		return c, sip.StatusForbidden
	}
	if a.opts.Tenant != "" && c.Tenant != a.opts.Tenant {
		return c, sip.StatusForbidden
	}
	if err := voice.Verify(a.opts.Keys, auth, a.now(), c.Tenant, c.Agent, c.SIPUser, c.Correlation, ts); err != nil {
		return c, sip.StatusForbidden
	}
	if err := a.replay.Check(c.Correlation, a.now()); err != nil {
		return c, sip.StatusForbidden
	}
	// With a runtime configured the agent answers only the personas it
	// loaded: talking-agent refuses an unknown number with 404.
	if a.opts.Base != "" {
		a.mu.Lock()
		known := false
		for _, ag := range a.view.Agents {
			if ag.SIPUser == c.SIPUser {
				known = true
				break
			}
		}
		a.mu.Unlock()
		if !known {
			return c, sip.StatusNotFound
		}
	}
	return c, 0
}

// hdr reads a request header's value ("" when absent).
func hdr(req *sip.Request, name string) string {
	h := req.GetHeader(name)
	if h == nil {
		return ""
	}
	return h.Value()
}

// reason is the response phrase for the codes the agent answers with.
func reason(code int) string {
	switch code {
	case sip.StatusOK:
		return "OK"
	case sip.StatusBadRequest:
		return "Bad Request"
	case sip.StatusForbidden:
		return "Forbidden"
	case sip.StatusNotFound:
		return "Not Found"
	case sip.StatusBusyHere:
		return "Busy Here"
	default:
		return "Server Error"
	}
}

// answerSDP builds the G.711 answer: the agent's RTP address and the offer's
// first codec (PCMU unless the offer leads with PCMA), ptime 20.
func (a *Agent) answerSDP(offer []byte) []byte {
	pt, codec := 0, "PCMU"
	if off, err := media.ParseAudioSDP(offer); err == nil && off.PayloadType == 8 {
		pt, codec = 8, "PCMA"
	}
	host := a.rtpAddr.IP.String()
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	o := a.now().Unix()
	return []byte(fmt.Sprintf(
		"v=0\r\no=fakeagent %d %d IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
			"m=audio %d RTP/AVP %d\r\na=rtpmap:%d %s/8000\r\na=ptime:20\r\n",
		o, o, host, host, a.rtpAddr.Port, pt, pt, codec))
}

// speak sends the tone to the offer's media address until the call ends.
func (a *Agent) speak(ctx context.Context, off media.AudioSDP, done <-chan struct{}) {
	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort(off.Address, strconv.Itoa(off.Port)))
	if dbg := os.Getenv("FAKEAGENT_DEBUG"); dbg != "" {
		fmt.Fprintf(os.Stderr, "fakeagent speak: offer=%+v dst=%v err=%v\n", off, dst, err)
	}
	if err != nil || off.Port == 0 {
		return
	}
	payload := tone20ms()
	pkt := make([]byte, 12, 12+len(payload))
	pkt[0] = 0x80 // V=2
	pkt[1] = off.PayloadType
	copy(pkt[8:12], rand4())
	var seq uint16
	var ts uint32
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-t.C:
		}
		pkt[2], pkt[3] = byte(seq>>8), byte(seq)
		pkt[4], pkt[5], pkt[6], pkt[7] = byte(ts>>24), byte(ts>>16), byte(ts>>8), byte(ts)
		if _, err := a.rtpConn.WriteTo(append(pkt[:0:12], payload...), dst); err != nil {
			return
		}
		seq++
		ts += uint32(len(payload)) //nolint:gosec // G115: len(payload) is 160
	}
}

// readRTP counts what Hello's relay sends, so a test can see the audio.
func (a *Agent) readRTP(ctx context.Context) {
	buf := make([]byte, 2048)
	for {
		if _, _, err := a.rtpConn.ReadFrom(buf); err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		a.rtpIn.Add(1)
	}
}

// report posts the call report once the dialog ended.
func (a *Agent) report(c Call) {
	r := Report{
		CorrelationID: c.Correlation,
		AgentName:     c.Agent,
		Outcome:       "answered",
		Summary:       "the fake talking-agent spoke with " + c.Caller,
		TokensIn:      42,
		TokensOut:     17,
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	if err := a.post(ctx, "/api/v1/voice-runtime/calls", r); err != nil {
		a.fail(fmt.Errorf("call report %s: %w", c.Correlation, err))
		return
	}
	a.mu.Lock()
	a.reports = append(a.reports, r)
	a.mu.Unlock()
}

// poll reads the runtime view forever: a plain read first, then long polls
// that return on a revision change or after Hello's wait, so a persona edit
// reaches the agent within two seconds (S-19). Every new revision is
// acknowledged (S-20).
func (a *Agent) poll(ctx context.Context) {
	rev := int64(-1)
	etag := ""
	for ctx.Err() == nil {
		v, err := a.fetch(ctx, rev, etag)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.fail(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if v == nil { // 304: nothing changed
			continue
		}
		a.setView(*v)
		rev, etag = v.Revision, v.ETag()
		if err := a.acknowledge(ctx, *v); err != nil {
			a.fail(err)
		}
	}
}

// fetch reads the view once: with a revision it long-polls and answers nil
// on 304, otherwise it returns the full view.
func (a *Agent) fetch(ctx context.Context, rev int64, etag string) (*voice.View, error) {
	u := a.opts.Base + "/api/v1/voice-runtime/agents"
	if rev >= 0 {
		u += "?wait=25&revision=" + strconv.FormatInt(rev, 10)
	}
	body, code, err := a.get(ctx, u, etag)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotModified {
		return nil, nil
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("voice-runtime agents = %d", code)
	}
	var v voice.View
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("voice-runtime agents: %w", err)
	}
	return &v, nil
}

// get does one authenticated GET, refreshing the credential once on 401.
func (a *Agent) get(ctx context.Context, u, etag string) ([]byte, int, error) {
	body, code, err := a.getOnce(ctx, u, etag)
	if code == http.StatusUnauthorized && a.opts.Token == "" {
		a.mu.Lock()
		a.bearer = ""
		a.mu.Unlock()
		return a.getOnce(ctx, u, etag)
	}
	return body, code, err
}

func (a *Agent) getOnce(ctx context.Context, u, etag string) ([]byte, int, error) {
	bearer, err := a.credential(ctx)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := a.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = res.Body.Close() }()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, res.StatusCode, err
	}
	return buf, res.StatusCode, nil
}

// acknowledge posts the ack of a loaded revision (S-20).
func (a *Agent) acknowledge(ctx context.Context, v voice.View) error {
	agents := make([]map[string]string, 0, len(v.Agents))
	for _, ag := range v.Agents {
		agents = append(agents, map[string]string{"name": ag.Name, "state": "loaded"})
	}
	err := a.post(ctx, "/api/v1/voice-runtime/ack", map[string]any{
		"revision": v.Revision, "version": a.opts.Version, "agents": agents,
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.acks = append(a.acks, v)
	a.mu.Unlock()
	return nil
}

// post does one authenticated POST and expects 2xx.
func (a *Agent) post(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	bearer, err := a.credential(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.opts.Base+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("POST %s = %d", path, res.StatusCode)
	}
	return nil
}

// credential returns the bearer token: the static one, or a client-
// credentials token refreshed half a minute before it expires.
func (a *Agent) credential(ctx context.Context) (string, error) {
	if a.opts.Token != "" {
		return a.opts.Token, nil
	}
	a.mu.Lock()
	tok, exp := a.bearer, a.bearerExp
	a.mu.Unlock()
	if tok != "" && a.now().Before(exp.Add(-30*time.Second)) {
		return tok, nil
	}
	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {string(auth.ScopeVoiceRuntime)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.opts.Base+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(a.opts.ClientID, a.opts.ClientSecret)
	res, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("client credentials = %d", res.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tr); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.bearer, a.bearerExp = tr.AccessToken, a.now().Add(time.Duration(tr.ExpiresIn)*time.Second)
	a.mu.Unlock()
	return tr.AccessToken, nil
}

// setView swaps the loaded view and marks the reload time.
func (a *Agent) setView(v voice.View) {
	a.mu.Lock()
	a.view, a.loaded = v, true
	a.mu.Unlock()
	a.lastDone.Store(a.now().UnixNano())
}

func (a *Agent) fail(err error) {
	a.mu.Lock()
	a.errs = append(a.errs, err)
	a.mu.Unlock()
}

// ended is closed when the dialog ends from either side (as test/sipua's).
func ended(d *sipgo.Dialog) <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	d.OnStateReplay(func(s sip.DialogState) {
		if s == sip.DialogStateEnded {
			once.Do(func() { close(ch) })
		}
	})
	return ch
}

// rand4 is four random bytes for the RTP SSRC.
func rand4() []byte {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A repeated SSRC harms nothing in a lab tone; keep going.
		return []byte("fke1")
	}
	return b[:]
}

// tone20ms is 160 micro-law samples of a 440 Hz sine: what the caller hears.
func tone20ms() []byte {
	const n = 160 // 20 ms at 8 kHz
	out := make([]byte, n)
	for i := range n {
		s := math.Sin(2 * math.Pi * 440 * float64(i) / 8000)
		out[i] = encodeMuLaw(s * 8000)
	}
	return out
}

// encodeMuLaw compiles one sample to G.711 micro-law.
func encodeMuLaw(s float64) byte {
	const bias = 132
	clamp := 32767
	if s < -32768 {
		s = -32768
	}
	if s > float64(clamp) {
		s = float64(clamp)
	}
	sign := byte(0x80)
	if s < 0 {
		sign, s = 0, -s
	}
	s += bias
	exp := byte(7)
	for mask := float64(0x4000); exp > 0 && int(s)&int(mask) == 0; mask /= 2 {
		exp--
	}
	mant := byte(int(s)>>int(exp+3)) & 0x0F //nolint:gosec // G115: the shift keeps the value in range
	return ^(sign | exp<<4 | mant)
}
