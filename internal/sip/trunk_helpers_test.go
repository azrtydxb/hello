package sip

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// --- fake trunk state ---------------------------------------------------------

type leaseRec struct {
	node  string
	until time.Time
}

// fakeTrunkState is livestate's trunk API in memory, with the same lease
// and slot semantics.
type fakeTrunkState struct {
	mu     sync.Mutex
	leases map[string]leaseRec
	regs   map[int64]livestate.TrunkRegistration
	health map[int64]map[string]livestate.DestinationHealth
	calls  map[int64]map[string]time.Time
	down   atomic.Bool
}

func newFakeTrunkState() *fakeTrunkState {
	return &fakeTrunkState{leases: map[string]leaseRec{}, regs: map[int64]livestate.TrunkRegistration{},
		health: map[int64]map[string]livestate.DestinationHealth{}, calls: map[int64]map[string]time.Time{}}
}

func (f *fakeTrunkState) AcquireLease(_ context.Context, key, node string, ttl time.Duration) (bool, error) {
	if f.down.Load() {
		return false, errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.leases[key]; ok && cur.node != node && time.Now().Before(cur.until) {
		return false, nil
	}
	f.leases[key] = leaseRec{node: node, until: time.Now().Add(ttl)}
	return true, nil
}

func (f *fakeTrunkState) ReleaseLease(_ context.Context, key, node string) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leases[key].node == node {
		delete(f.leases, key)
	}
	return nil
}

func (f *fakeTrunkState) holder(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.leases[livestate.TrunkLeaseKey(id)]
	if !ok || time.Now().After(l.until) {
		return ""
	}
	return l.node
}

func (f *fakeTrunkState) PutTrunkRegistration(_ context.Context, id int64, r livestate.TrunkRegistration, _ time.Duration) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regs[id] = r
	return nil
}

func (f *fakeTrunkState) PutDestinationHealth(_ context.Context, id int64, h livestate.DestinationHealth, _ time.Duration) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.health[id] == nil {
		f.health[id] = map[string]livestate.DestinationHealth{}
	}
	f.health[id][h.Destination] = h
	return nil
}

func (f *fakeTrunkState) AcquireTrunkCall(_ context.Context, id int64, call string, maxCalls int, ttl time.Duration) (bool, error) {
	if f.down.Load() {
		return false, errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.calls[id] == nil {
		f.calls[id] = map[string]time.Time{}
	}
	for c, exp := range f.calls[id] {
		if !exp.After(now) {
			delete(f.calls[id], c)
		}
	}
	if _, held := f.calls[id][call]; maxCalls > 0 && !held && len(f.calls[id]) >= maxCalls {
		return false, nil
	}
	f.calls[id][call] = now.Add(ttl)
	return true, nil
}

func (f *fakeTrunkState) RefreshTrunkCall(_ context.Context, id int64, call string, ttl time.Duration) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.calls[id][call]; ok {
		f.calls[id][call] = time.Now().Add(ttl)
	}
	return nil
}

func (f *fakeTrunkState) ReleaseTrunkCall(_ context.Context, id int64, call string) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.calls[id], call)
	return nil
}

func (f *fakeTrunkState) activeCalls(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, exp := range f.calls[id] {
		if exp.After(time.Now()) {
			n++
		}
	}
	return n
}

func (f *fakeTrunkState) registration(id int64) (livestate.TrunkRegistration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.regs[id]
	return r, ok
}

func (f *fakeTrunkState) destHealth(id int64, dest string) (livestate.DestinationHealth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.health[id][dest]
	return h, ok
}

func (f *fakeTrunkState) TrunkStatus(_ context.Context, id int64) (livestate.TrunkStatus, error) {
	if f.down.Load() {
		return livestate.TrunkStatus{}, errDown
	}
	st := livestate.TrunkStatus{TrunkID: id, Destinations: []livestate.DestinationHealth{}}
	f.mu.Lock()
	if r, ok := f.regs[id]; ok {
		st.Registration = &r
	}
	for _, h := range f.health[id] {
		st.Destinations = append(st.Destinations, h)
	}
	f.mu.Unlock()
	st.ActiveCalls = f.activeCalls(id)
	return st, nil
}

// nodeView is one node's connection to the shared fake state; cutting it
// simulates that node dying without releasing anything.
type nodeView struct {
	*fakeTrunkState
	cut atomic.Bool
}

func (v *nodeView) AcquireLease(ctx context.Context, key, node string, ttl time.Duration) (bool, error) {
	if v.cut.Load() {
		return false, errors.New("node gone")
	}
	return v.fakeTrunkState.AcquireLease(ctx, key, node, ttl)
}

// --- fake router ------------------------------------------------------------

// fakeRouter stands in for the routing engine (not on this branch): it
// routes internal extensions like the engine and delegates everything else
// to decide.
type fakeRouter struct {
	mu         sync.Mutex
	extensions map[string]bool
	trunks     []routing.Trunk
	sources    map[netip.Addr]int64
	decide     func(c routing.Call, usable routing.TrunkUsability) routing.Decision
}

// setDecide replaces the decision function while the PBX runs.
func (r *fakeRouter) setDecide(f func(routing.Call, routing.TrunkUsability) routing.Decision) {
	r.mu.Lock()
	r.decide = f
	r.mu.Unlock()
}

// setSource maps a source IP to a trunk while the PBX runs.
func (r *fakeRouter) setSource(ip netip.Addr, id int64) {
	r.mu.Lock()
	if r.sources == nil {
		r.sources = map[netip.Addr]int64{}
	}
	r.sources[ip] = id
	r.mu.Unlock()
}

func (r *fakeRouter) Decide(c routing.Call, usable routing.TrunkUsability) routing.Decision {
	r.mu.Lock()
	decide := r.decide
	r.mu.Unlock()
	if c.FromExtension != "" && r.extensions[c.Number] {
		var d routing.Decision
		d.Trace.Add("Internal extension lookup \"" + c.Number + "\" -> extension " + c.Number)
		d.Kind, d.Extension = routing.KindInternal, c.Number
		return d
	}
	if decide == nil {
		var d routing.Decision
		d.Kind, d.RejectCode, d.Reason = routing.KindReject, 404, "no route matched"
		return d
	}
	return decide(c, usable)
}

func (r *fakeRouter) Trunk(id int64) (*routing.Trunk, bool) {
	for i := range r.trunks {
		if r.trunks[i].ID == id {
			return &r.trunks[i], true
		}
	}
	return nil, false
}

func (r *fakeRouter) TrunkForSource(ip netip.Addr, _ string) (*routing.Trunk, bool) {
	r.mu.Lock()
	id, ok := r.sources[ip]
	r.mu.Unlock()
	if ok {
		return r.Trunk(id)
	}
	return nil, false
}

// outboundVia builds an outbound decision over the given trunks the way the
// engine does: unusable trunks are skipped and traced; none left is 503.
func outboundVia(r *fakeRouter, number, callerID string, emergency bool, codes []int, ids ...int64) func(routing.Call, routing.TrunkUsability) routing.Decision {
	return func(_ routing.Call, usable routing.TrunkUsability) routing.Decision {
		d := routing.Decision{Kind: routing.KindOutbound, Number: number, CallerID: callerID, Route: "Test route",
			FailoverCodes: codes, Emergency: emergency}
		d.Trace.Add(`Route "Test route" matched (prefix 0)`)
		for _, id := range ids {
			t, _ := r.Trunk(id)
			if ok, why := usable(id, emergency); !ok {
				d.Trace.Add("Trunk " + t.Name + " skipped: " + why)
				continue
			}
			d.Candidates = append(d.Candidates, routing.Candidate{Trunk: t, Destinations: t.Destinations})
		}
		if len(d.Candidates) == 0 {
			d.Kind, d.RejectCode, d.Reason = routing.KindReject, 503, "no usable trunk"
		}
		return d
	}
}

// withRouter installs r (and its trunks) in the PBX's snapshot.
func withRouter(r *fakeRouter, bad map[int64]string) pbxOpt {
	return func(_ *Config, d *Deps) {
		snaps := d.Snapshots.(*fakeSnaps)
		cur := snaps.p.Load()
		cur.WithRouting(&snapshot.RoutingState{Router: r, Config: routing.Config{Trunks: r.trunks}, Misconfigured: bad})
	}
}

// trunkCfg makes trunk tests fast.
func trunkCfg(st TrunkState) pbxOpt {
	return func(c *Config, d *Deps) {
		d.Trunks = st
		c.TrunkLeaseRefresh, c.TrunkStatusPoll = 100*time.Millisecond, 30*time.Millisecond
		c.TrunkAttemptTimeout, c.TrunkRetryBase, c.TrunkRetryMax = 500*time.Millisecond, 200*time.Millisecond, time.Second
		c.TrunkOptionsTimeout = time.Second
		c.CallHeartbeat = 50 * time.Millisecond
	}
}

// --- fake carrier -----------------------------------------------------------

// carrier is a sipgo UAS on loopback acting as a SIP trunk provider: a
// registrar with digest, OPTIONS, and scripted INVITE outcomes.
type carrier struct {
	t     *testing.T
	addr  string
	host  string
	port  int
	user  string
	pass  string
	realm string
	ua    *sipgo.UserAgent
	srv   *sipgo.Server
	dua   *sipgo.DialogUA
	sdp   string

	grant       atomic.Int64 // registration expiry granted, seconds
	inviteCode  atomic.Int64 // 200 answer, 0 ring forever, -1 stay silent, else that code
	challenge   atomic.Bool  // answer INVITE with 407 first
	optionsDown atomic.Bool  // do not answer OPTIONS

	mu       sync.Mutex
	regs     []*sip.Request // authorized REGISTERs
	options  int
	dialogs  map[string]*sipgo.DialogServerSession
	invites  chan *sip.Request
	cancels  chan *sip.Request
	byes     chan *sip.Request
	acks     chan *sip.Request
	rejected atomic.Int64 // requests with bad credentials
}

func newCarrier(t *testing.T, user, pass string) *carrier {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	host, port, _ := sip.ParseAddr(addr)
	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("carrier"),
		sipgo.WithUserAgentTransactionLayerOptions(sip.WithTransactionLayerLogger(discard)),
		sipgo.WithUserAgentTransportLayerOptions(sip.WithTransportLayerLogger(discard)))
	srv, _ := sipgo.NewServer(ua, sipgo.WithServerLogger(discard))
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientAddr(addr), sipgo.WithClientConnectionAddr(addr), sipgo.WithClientLogger(discard))
	cr := &carrier{t: t, addr: addr, host: host, port: port, user: user, pass: pass, realm: "carrier.test", ua: ua, srv: srv,
		sdp:     "v=0\r\no=carrier-" + strconv.Itoa(port) + " 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\n",
		dialogs: map[string]*sipgo.DialogServerSession{}, invites: make(chan *sip.Request, 16), cancels: make(chan *sip.Request, 16),
		byes: make(chan *sip.Request, 16), acks: make(chan *sip.Request, 16)}
	cr.dua = &sipgo.DialogUA{Client: cli, ContactHDR: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "carrier", Host: host, Port: port}}}
	cr.grant.Store(60)
	cr.inviteCode.Store(200)
	srv.OnRegister(cr.onRegister)
	srv.OnOptions(cr.onOptions)
	srv.OnInvite(cr.onInvite)
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		push(cr.acks, req)
		if d := cr.dialog(req); d != nil {
			_ = d.ReadAck(req, tx)
		}
	})
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		push(cr.byes, req)
		if d := cr.dialog(req); d != nil && d.ReadBye(req, tx) == nil {
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() { _ = srv.ServeUDP(conn) }()
	t.Cleanup(func() { _ = conn.Close(); _ = ua.Close() })
	eventually(t, "carrier listener", func() bool { c, _ := ua.TransportLayer().GetConnection("udp", addr); return c != nil })
	return cr
}

func (cr *carrier) dialog(req *sip.Request) *sipgo.DialogServerSession {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return cr.dialogs[req.CallID().Value()]
}

func (cr *carrier) dest() routing.Destination {
	return routing.Destination{Host: cr.host, Port: cr.port, Weight: 1}
}

// verify checks digest credentials in header h against the carrier's.
func (cr *carrier) verify(req *sip.Request, h string) bool {
	hdr := req.GetHeader(h)
	if hdr == nil {
		return false
	}
	creds, err := digest.ParseCredentials(hdr.Value())
	if err != nil || creds.Username != cr.user {
		return false
	}
	chal := &digest.Challenge{Realm: cr.realm, Nonce: creds.Nonce, Algorithm: creds.Algorithm, QOP: []string{creds.QOP}}
	want, err := digest.Digest(chal, digest.Options{Method: req.Method.String(), URI: creds.URI, Username: cr.user,
		Password: cr.pass, Cnonce: creds.Cnonce, Count: creds.Nc})
	return err == nil && want.Response == creds.Response
}

func (cr *carrier) challengeHeader() string {
	c := digest.Challenge{Realm: cr.realm, Nonce: sip.GenerateTagN(12), Algorithm: "MD5", QOP: []string{"auth"}}
	return c.String()
}

func (cr *carrier) onRegister(req *sip.Request, tx sip.ServerTransaction) {
	if req.GetHeader("Authorization") == nil {
		res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", cr.challengeHeader()))
		_ = tx.Respond(res)
		return
	}
	if !cr.verify(req, "Authorization") {
		cr.rejected.Add(1)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 403, "Forbidden", nil))
		return
	}
	cr.mu.Lock()
	cr.regs = append(cr.regs, req)
	cr.mu.Unlock()
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	if c := req.Contact(); c != nil {
		cc := c.Clone()
		cc.Params = sip.HeaderParams{{K: "expires", V: strconv.FormatInt(cr.grant.Load(), 10)}}
		res.AppendHeader(cc)
	}
	_ = tx.Respond(res)
}

// registrations returns the authorized REGISTERs with a non-zero expiry.
func (cr *carrier) registrations() []*sip.Request {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	var out []*sip.Request
	for _, r := range cr.regs {
		if h := r.GetHeader("Expires"); h == nil || h.Value() != "0" {
			out = append(out, r)
		}
	}
	return out
}

func (cr *carrier) onOptions(req *sip.Request, tx sip.ServerTransaction) {
	cr.mu.Lock()
	cr.options++
	cr.mu.Unlock()
	if cr.optionsDown.Load() {
		return // no response: a dead carrier
	}
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

func (cr *carrier) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	if _, ok := req.To().Params.Get("tag"); ok {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", req.Body()))
		return
	}
	if cr.challenge.Load() && !cr.verify(req, "Proxy-Authorization") {
		if req.GetHeader("Proxy-Authorization") != nil {
			cr.rejected.Add(1)
		}
		res := sip.NewResponseFromRequest(req, 407, "Proxy Authentication Required", nil)
		res.AppendHeader(sip.NewHeader("Proxy-Authenticate", cr.challengeHeader()))
		_ = tx.Respond(res)
		return
	}
	push(cr.invites, req)
	code := cr.inviteCode.Load()
	if code == -1 {
		<-tx.Done() // silent: only the transaction layer's 100 Trying
		return
	}
	dss, err := cr.dua.ReadInvite(req, tx)
	if err != nil {
		return
	}
	cr.mu.Lock()
	cr.dialogs[req.CallID().Value()] = dss
	cr.mu.Unlock()
	switch code {
	case 200:
		_ = dss.RespondSDP([]byte(cr.sdp))
	case 0:
		_ = dss.Respond(180, "Ringing", nil)
		<-dss.Context().Done()
		push(cr.cancels, req)
	default:
		_ = dss.Respond(int(code), statusText(int(code)), nil)
	}
}

func (cr *carrier) trunk(id int64, name, mode string) routing.Trunk {
	return routing.Trunk{ID: id, Name: name, Mode: mode, Username: cr.user, Password: cr.pass, Realm: cr.realm,
		FromDomain: name + ".example", RegisterExpires: 60 * time.Second, OptionsInterval: time.Second,
		Enabled: true, Destinations: []routing.Destination{cr.dest()}}
}

// destKey is the health key of the carrier's destination.
func (cr *carrier) destKey() string { return snapshot.DestKey(cr.dest()) }
