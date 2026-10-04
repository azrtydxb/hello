package sip

import (
	"context"
	"crypto/md5" //nolint:gosec // digest HA1 for tests.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	"github.com/prometheus/client_golang/prometheus"
)

const testDomain = "hello.test"

// testSecret is a throwaway nonce key, long enough for New.
var testSecret = strings.Repeat("k", 40)

var discard = slog.New(slog.DiscardHandler)

func TestMain(m *testing.M) {
	sip.SetDefaultLogger(discard)
	os.Exit(m.Run())
}

// --- fakes -----------------------------------------------------------------

var errDown = errors.New("valkey down")

type fakeState struct {
	mu    sync.Mutex
	regs  map[string]map[string]livestate.Binding
	calls map[string]livestate.Call
	down  atomic.Bool
	// putGate, when set, holds PutCall until the channel is closed;
	// putBlocked is signalled when one is held.
	putGate    atomic.Pointer[chan struct{}]
	putBlocked chan struct{}
}

func newFakeState() *fakeState {
	return &fakeState{regs: map[string]map[string]livestate.Binding{}, calls: map[string]livestate.Call{}, putBlocked: make(chan struct{}, 1)}
}

func (f *fakeState) PutBinding(_ context.Context, b livestate.Binding) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !b.Expires.After(time.Now()) {
		delete(f.regs[b.AOR], b.ContactURI)
		return nil
	}
	if f.regs[b.AOR] == nil {
		f.regs[b.AOR] = map[string]livestate.Binding{}
	}
	f.regs[b.AOR][b.ContactURI] = b
	return nil
}

func (f *fakeState) DeleteBinding(_ context.Context, aor, uri string) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.regs[aor], uri)
	return nil
}

func (f *fakeState) DeleteAOR(_ context.Context, aor string) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.regs, aor)
	return nil
}

func (f *fakeState) Bindings(_ context.Context, aor string) ([]livestate.Binding, error) {
	if f.down.Load() {
		return nil, errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []livestate.Binding
	for _, b := range f.regs[aor] {
		if b.Expires.After(time.Now()) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeState) AllBindings(ctx context.Context) ([]livestate.Binding, error) {
	f.mu.Lock()
	aors := make([]string, 0, len(f.regs))
	for a := range f.regs {
		aors = append(aors, a)
	}
	f.mu.Unlock()
	var out []livestate.Binding
	for _, a := range aors {
		bs, err := f.Bindings(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

func (f *fakeState) PutCall(_ context.Context, c livestate.Call, _ time.Duration) error {
	if f.down.Load() {
		return errDown
	}
	if g := f.putGate.Load(); g != nil {
		select {
		case f.putBlocked <- struct{}{}:
		default:
		}
		<-*g
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[c.ID] = c
	return nil
}

func (f *fakeState) DeleteCall(_ context.Context, id string) error {
	if f.down.Load() {
		return errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.calls, id)
	return nil
}

func (f *fakeState) Calls(context.Context) ([]livestate.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]livestate.Call, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c)
	}
	return out, nil
}

type fakeThrottle struct {
	mu   sync.Mutex
	n    map[string]int64
	nc   map[string]int64
	down atomic.Bool
	// ncDown fails only the nonce-count store.
	ncDown atomic.Bool
	// barrier, when set, holds Failures until that many callers are in
	// it (or a second passes), so concurrent attempts all pass the
	// precheck together.
	barrier atomic.Pointer[barrier]
}

type barrier struct {
	mu      sync.Mutex
	need    int
	release chan struct{}
}

func newBarrier(n int) *barrier { return &barrier{need: n, release: make(chan struct{})} }

func (b *barrier) wait() {
	b.mu.Lock()
	b.need--
	if b.need == 0 {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-time.After(time.Second):
	}
}

func (f *fakeThrottle) Failures(_ context.Context, ip string) (int64, error) {
	if f.down.Load() {
		return 0, errDown
	}
	if b := f.barrier.Load(); b != nil {
		b.wait()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[ip], nil
}

func (f *fakeThrottle) RecordFailure(_ context.Context, ip string) (int64, error) {
	if f.down.Load() {
		return 0, errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == nil {
		f.n = map[string]int64{}
	}
	f.n[ip]++
	return f.n[ip], nil
}

func (f *fakeThrottle) AdvanceNonceCount(_ context.Context, key string, nc int64, _ time.Duration) (bool, error) {
	if f.down.Load() || f.ncDown.Load() {
		return false, errDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nc == nil {
		f.nc = map[string]int64{}
	}
	if nc <= f.nc[key] {
		return false, nil
	}
	f.nc[key] = nc
	return true, nil
}

type fakeCDRs struct {
	ch chan cdr.Record
}

func (f *fakeCDRs) Enqueue(r cdr.Record) bool {
	select {
	case f.ch <- r:
		return true
	default:
		return false
	}
}

// fakePresence is a shared node registry; extra lists addresses to treat
// as nodes without a Server behind them.
type fakePresence struct {
	mu    sync.Mutex
	nodes map[string]string
	extra []string
}

func newFakePresence() *fakePresence { return &fakePresence{nodes: map[string]string{}} }

func (f *fakePresence) Announce(_ context.Context, id, addr string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[id] = addr
	return nil
}

func (f *fakePresence) Nodes(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.extra...)
	for _, a := range f.nodes {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakePresence) add(addr string) {
	f.mu.Lock()
	f.extra = append(f.extra, addr)
	f.mu.Unlock()
}

type fakeSnaps struct {
	p     atomic.Pointer[snapshot.Snapshot]
	reads atomic.Int64
}

func (f *fakeSnaps) Current() *snapshot.Snapshot {
	f.reads.Add(1)
	return f.p.Load()
}

// --- devices -----------------------------------------------------------------

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func dev(id int64, user, ext, password string) snapshot.Device {
	m := md5.Sum([]byte(user + ":" + testDomain + ":" + password)) //nolint:gosec // digest.
	s := sha256.Sum256([]byte(user + ":" + testDomain + ":" + password))
	return snapshot.Device{
		ID: id, Username: user, Realm: testDomain, HA1MD5: hexOf(m[:]), HA1SHA256: hexOf(s[:]),
		Extension: ext, ExtensionName: "Ext " + ext,
	}
}

// --- PBX under test ----------------------------------------------------------

type testPBX struct {
	srv      *Server
	addr     string
	cfg      Config
	state    State
	fake     *fakeState // nil when state is real Valkey
	throttle Throttle
	cdrs     *fakeCDRs
	snaps    *fakeSnaps
	reg      *prometheus.Registry
	m        *Metrics
	conn     net.PacketConn
}

type pbxOpt func(*Config, *Deps)

func startPBX(t *testing.T, devices []snapshot.Device, opts ...pbxOpt) *testPBX {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	reg := prometheus.NewRegistry()
	fs := newFakeState()
	snaps := &fakeSnaps{}
	// Extension 599 exists without any enabled device.
	snaps.p.Store(snapshot.New(1, testDomain, devices).WithExtensions(bareExtension))
	cfg := Config{
		NodeID: "sip-test", Domain: testDomain, AdvertisedAddr: addr, NonceSecret: []byte(testSecret),
		MinExpires: 60 * time.Second, MaxExpires: time.Hour, RingTimeout: 5 * time.Second,
		AuthFailLimit: 10, StateTimeout: 200 * time.Millisecond, RecountInterval: 50 * time.Millisecond,
		PeerRefresh: 50 * time.Millisecond,
	}
	deps := Deps{
		Snapshots: snaps, State: fs, Throttle: &fakeThrottle{n: map[string]int64{}},
		CDRs: &fakeCDRs{ch: make(chan cdr.Record, 64)}, Metrics: NewMetrics(reg), Log: discard,
	}
	for _, o := range opts {
		o(&cfg, &deps)
	}
	srv, err := New(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, conn) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	p := &testPBX{srv: srv, addr: addr, cfg: cfg, state: deps.State, throttle: deps.Throttle,
		cdrs: deps.CDRs.(*fakeCDRs), snaps: snaps, reg: reg, m: deps.Metrics, conn: conn}
	p.fake, _ = deps.State.(*fakeState)
	return p
}

func (p *testPBX) nextCDR(t *testing.T) cdr.Record {
	t.Helper()
	select {
	case r := <-p.cdrs.ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("no CDR")
		return cdr.Record{}
	}
}

func (p *testPBX) noCDR(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case r := <-p.cdrs.ch:
		t.Fatalf("unexpected extra CDR %+v", r)
	case <-time.After(d):
	}
}

// metric returns the value of a counter or gauge sample, 0 when absent.
func (p *testPBX) metric(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := p.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if v, ok := labels[lp.GetName()]; ok && v != lp.GetValue() {
					continue next
				}
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return 0
}

// --- test phone -------------------------------------------------------------

type calleeFn func(p *phone, req *sip.Request, tx sip.ServerTransaction, dss *sipgo.DialogServerSession)

// phone is a sipgo UA on loopback acting as a desk phone.
type phone struct {
	t       *testing.T
	user    string
	pass    string
	addr    string
	pbx     string
	sdp     string
	ua      *sipgo.UserAgent
	srv     *sipgo.Server
	cli     *sipgo.Client
	dua     *sipgo.DialogUA
	contact sip.ContactHeader

	mu     sync.Mutex
	callee calleeFn
	// reinviteHold, when set, delays the 200 to re-INVITE/UPDATE until
	// closed; reinviteAnswered is stamped just before that 200 goes out, so
	// a BYE arriving at any phone is never before it.
	reinviteHold     chan struct{}
	reinviteAnswered time.Time
	byeAt            time.Time
	servers          map[string]*sipgo.DialogServerSession
	clients          map[string]*sipgo.DialogClientSession

	invites   chan *sip.Request
	reinvites chan *sip.Request
	cancels   chan *sip.Request
	acks      chan *sip.Request
	byes      chan *sip.Request
	rings     chan int // provisional codes the phone heard as caller
}

func newPhone(t *testing.T, pbx *testPBX, user, pass string) *phone {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return startPhone(t, pbx, user, pass, conn)
}

// natConn drops every datagram not from allow, like a NAT that only has a
// mapping for the node the phone registered with.
type natConn struct {
	net.PacketConn
	allow   string
	dropped atomic.Int64
}

func (c *natConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(b)
		if err != nil || addr.String() == c.allow {
			return n, addr, err
		}
		c.dropped.Add(1)
	}
}

// newNATPhone is a phone reachable only from pbx.
func newNATPhone(t *testing.T, pbx *testPBX, user, pass string) (*phone, *natConn) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	nc := &natConn{PacketConn: conn, allow: pbx.addr}
	return startPhone(t, pbx, user, pass, nc), nc
}

func startPhone(t *testing.T, pbx *testPBX, user, pass string, conn net.PacketConn) *phone {
	t.Helper()
	addr := conn.LocalAddr().String()
	host, port, _ := sip.ParseAddr(addr)
	ua, err := sipgo.NewUA(sipgo.WithUserAgent(user), sipgo.WithUserAgentHostname(testDomain),
		sipgo.WithUserAgentTransactionLayerOptions(sip.WithTransactionLayerLogger(discard)),
		sipgo.WithUserAgentTransportLayerOptions(sip.WithTransportLayerLogger(discard)))
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := sipgo.NewServer(ua, sipgo.WithServerLogger(discard))
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientAddr(addr), sipgo.WithClientConnectionAddr(addr), sipgo.WithClientLogger(discard))
	p := &phone{
		t: t, user: user, pass: pass, addr: addr, pbx: pbx.addr, ua: ua, srv: srv, cli: cli,
		sdp:     "v=0\r\no=" + user + " 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + strconv.Itoa(port+1000) + " RTP/AVP 0\r\n",
		contact: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: host, Port: port}},
		servers: map[string]*sipgo.DialogServerSession{}, clients: map[string]*sipgo.DialogClientSession{},
		invites: make(chan *sip.Request, 16), reinvites: make(chan *sip.Request, 16), cancels: make(chan *sip.Request, 16),
		acks: make(chan *sip.Request, 16), byes: make(chan *sip.Request, 16), rings: make(chan int, 16),
		callee: answerAfter(nil),
	}
	p.dua = &sipgo.DialogUA{Client: cli, ContactHDR: p.contact, RewriteContact: true}
	srv.OnInvite(p.onInvite)
	srv.OnAck(p.onAck)
	srv.OnBye(p.onBye)
	srv.OnUpdate(p.onReinvite)
	go func() { _ = srv.ServeUDP(conn) }()
	t.Cleanup(func() {
		_ = conn.Close()
		_ = ua.Close()
	})
	// Requests must leave from the listener; wait until sipgo serves it.
	eventually(t, "phone listener", func() bool {
		c, _ := ua.TransportLayer().GetConnection("udp", addr)
		return c != nil
	})
	return p
}

func push(ch chan *sip.Request, r *sip.Request) {
	select {
	case ch <- r:
	default:
	}
}

func (p *phone) setCallee(f calleeFn) {
	p.mu.Lock()
	p.callee = f
	p.mu.Unlock()
}

func (p *phone) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	if _, ok := req.To().Params.Get("tag"); ok {
		p.onReinvite(req, tx)
		return
	}
	dss, err := p.dua.ReadInvite(req, tx)
	if err != nil {
		p.t.Errorf("%s: read invite: %v", p.user, err)
		return
	}
	p.mu.Lock()
	p.servers[req.CallID().Value()] = dss
	f := p.callee
	p.mu.Unlock()
	push(p.invites, req)
	f(p, req, tx, dss)
}

func (p *phone) onReinvite(req *sip.Request, tx sip.ServerTransaction) {
	push(p.reinvites, req)
	p.mu.Lock()
	hold := p.reinviteHold
	p.mu.Unlock()
	if hold != nil {
		<-hold // the test decides when the 200 goes out
	}
	// Stamp before the 200 goes out: a BYE can only arrive after this
	// instant, so a test that has seen the BYE has also seen this stamp.
	// Stamping after tx.Respond raced the reader: a starved answerer could
	// be descheduled between the send and the stamp, past the reader.
	p.mu.Lock()
	p.reinviteAnswered = time.Now()
	p.mu.Unlock()
	res := sip.NewResponseFromRequest(req, 200, "OK", req.Body())
	if ct := req.ContentType(); ct != nil {
		res.AppendHeader(sip.HeaderClone(ct))
	}
	res.AppendHeader(sip.HeaderClone(&p.contact))
	_ = tx.Respond(res)
}

func (p *phone) onAck(req *sip.Request, tx sip.ServerTransaction) {
	push(p.acks, req)
	p.mu.Lock()
	dss := p.servers[req.CallID().Value()]
	p.mu.Unlock()
	if dss != nil {
		_ = dss.ReadAck(req, tx)
	}
}

func (p *phone) onBye(req *sip.Request, tx sip.ServerTransaction) {
	p.mu.Lock()
	p.byeAt = time.Now()
	p.mu.Unlock()
	push(p.byes, req)
	p.mu.Lock()
	dss := p.servers[req.CallID().Value()]
	dcs := p.clients[req.CallID().Value()]
	p.mu.Unlock()
	switch {
	case dss != nil && dss.ReadBye(req, tx) == nil:
	case dcs != nil && dcs.ReadBye(req, tx) == nil:
	default:
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	}
}

// answerAfter rings, then answers with the phone's SDP once ch is closed (or
// at once when ch is nil). It stops ringing when the INVITE is cancelled.
func answerAfter(ch <-chan struct{}) calleeFn {
	return func(p *phone, req *sip.Request, tx sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		if ch != nil {
			_ = dss.Respond(180, "Ringing", nil)
			select {
			case <-ch:
			case <-dss.Context().Done():
				push(p.cancels, req)
				return
			}
		}
		_ = dss.RespondSDP([]byte(p.sdp))
	}
}

func busy() calleeFn {
	return func(_ *phone, _ *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(486, "Busy Here", nil)
	}
}

func ringForever() calleeFn { return answerAfter(make(chan struct{})) }

// answerOnCancel rings; when the CANCEL arrives it answers 200 on the wire
// before the transaction's 487, reproducing a 200 crossing the CANCEL.
func answerOnCancel() calleeFn {
	return func(p *phone, req *sip.Request, tx sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(180, "Ringing", nil)
		tx.OnCancel(func(*sip.Request) {
			res := sip.NewResponseFromRequest(dss.InviteRequest, 200, "OK", []byte(p.sdp))
			res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			res.AppendHeader(sip.HeaderClone(&p.contact))
			_ = p.srv.WriteResponse(res)
			// Let the 200 be processed before the transaction's 487: sipgo
			// handles each datagram on its own goroutine, and a 487 handled
			// first would complete the client transaction and drop the 200.
			time.Sleep(100 * time.Millisecond)
		})
		<-dss.Context().Done()
		push(p.cancels, req)
	}
}

func (p *phone) aorURI() sip.Uri { return sip.Uri{Scheme: "sip", User: p.user, Host: testDomain} }

// do sends req and returns the final response.
func (p *phone) do(req *sip.Request) *sip.Response {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := p.cli.Do(ctx, req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", p.user, req.Method, err)
	}
	return res
}

// pickChallenge returns the WWW-Authenticate challenge for alg.
func pickChallenge(t *testing.T, res *sip.Response, alg string) *digest.Challenge {
	t.Helper()
	for _, h := range res.GetHeaders("WWW-Authenticate") {
		c, err := digest.ParseChallenge(h.Value())
		if err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(c.Algorithm, alg) {
			return c
		}
	}
	t.Fatalf("no %s challenge in %d response", alg, res.StatusCode)
	return nil
}

func authorize(t *testing.T, req *sip.Request, chal *digest.Challenge, user, pass string) {
	t.Helper()
	cred, err := digest.Digest(chal, digest.Options{Method: req.Method.String(), URI: req.Recipient.String(), Username: user, Password: pass})
	if err != nil {
		t.Fatal(err)
	}
	req.RemoveHeader("Authorization")
	req.AppendHeader(sip.NewHeader("Authorization", cred.String()))
	req.CSeq().SeqNo++
	req.RemoveHeader("Via")
}

// authDo sends req; on 401 it answers the alg challenge with pass and resends.
func (p *phone) authDo(req *sip.Request, alg, pass string) *sip.Response {
	p.t.Helper()
	res := p.do(req)
	if res.StatusCode != 401 {
		return res
	}
	authorize(p.t, req, pickChallenge(p.t, res, alg), p.user, pass)
	return p.do(req)
}

func (p *phone) registerReq(expires int, contacts ...string) *sip.Request {
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: p.aorURI(), Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: p.aorURI()})
	if len(contacts) == 0 {
		contacts = []string{"<" + p.contact.Address.String() + ">"}
	}
	for _, c := range contacts {
		if c != "" { // "" means: no Contact (a query)
			req.AppendHeader(sip.NewHeader("Contact", c))
		}
	}
	if expires >= 0 {
		req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
	}
	req.SetDestination(p.pbx)
	return req
}

func (p *phone) register(t *testing.T) {
	t.Helper()
	if res := p.authDo(p.registerReq(300), AlgSHA256, p.pass); res.StatusCode != 200 {
		t.Fatalf("%s register = %d", p.user, res.StatusCode)
	}
}

// call dials ext through the PBX (outbound-proxy style Route header) and
// waits for the final answer; on 2xx it ACKs.
func (p *phone) call(ctx context.Context, ext string) (*sipgo.DialogClientSession, error) {
	req := p.inviteReq(ext)
	dcs, err := p.dua.WriteInvite(ctx, req)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.clients[req.CallID().Value()] = dcs
	p.mu.Unlock()
	heard := func(r *sip.Response) error {
		if r.IsProvisional() {
			select {
			case p.rings <- r.StatusCode:
			default:
			}
		}
		return nil
	}
	if err := dcs.WaitAnswer(ctx, sipgo.AnswerOptions{Username: p.user, Password: p.pass, OnResponse: heard}); err != nil {
		return dcs, err
	}
	return dcs, dcs.Ack(context.Background())
}

func (p *phone) inviteReq(ext string) *sip.Request {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: ext, Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: p.aorURI(), Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: ext, Host: testDomain}})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(sip.NewHeader("Route", "<sip:"+p.pbx+";lr>"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(p.sdp))
	return req
}

func waitReq(t *testing.T, ch chan *sip.Request, what string) *sip.Request {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

func noReq(t *testing.T, ch chan *sip.Request, d time.Duration, what string) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("unexpected %s: %s", what, r.StartLine())
	case <-time.After(d):
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
