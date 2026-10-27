// Package kindha proves the Kamailio pair's failover on a kind cluster
// (.procoder/specs/kamailio-ha.md, acceptance criteria). It runs only in the
// kamailio-ha CI job (HELLO_KINDHA=1), which creates the cluster, loads the
// Kamailio and kube-vip images and starts this test.
//
// The test applies the kw manifests for the edge (two hostNetwork Kamailio, a
// kube-vip sidecar each, the VIP) and plays both sides of the edge itself:
// phones on one side, Hello nodes (a registrar and a B2BUA stand-in) on the
// other. That keeps the proof on what a failover changes: the addresses that
// dialogs name (Via, Path, Record-Route) and the VIP that follows a healthy
// instance. Hello's own takeover logic is proven elsewhere; what it needs from
// the edge is that its route set still reaches a live Kamailio, which is what
// the re-INVITE and BYE here do.
package kindha

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

const domain = "hello.kind"

// vipFailoverBound is how long the VIP may take to answer from the surviving
// instance after the active one is lost (the Lease is 5 s; the rest is the
// Kamailio start and the neighbours' ARP caches). A deleted pod's kube-vip
// may hold the Lease until its container is stopped (up to the pod's 30 s
// grace) on a loaded runner, so the bound is generous; the logged times are
// the measurement, and the acceptance bound that matters (a BYE within 5 s of
// the VIP moving) is byeBound.
const vipFailoverBound = 60 * time.Second

// byeBound is the acceptance bound: a BYE ends a call within 5 s of the VIP
// moving.
const byeBound = 5 * time.Second

type dialog struct {
	callID   string
	local    string // From of our requests (with our tag)
	remote   string // To of our requests (with theirs)
	target   string // Request-URI
	routes   []string
	cseq     int
	contact  string
	inviteRR []string // the Record-Route of the INVITE that made the dialog, as its UAS saw it
}

func reversed(l []string) []string {
	out := make([]string, len(l))
	for i, v := range l {
		out[len(l)-1-i] = v
	}
	return out
}

// dst is where a request in the dialog goes first: its first Route, else the
// target.
func (d *dialog) dst() string {
	hop := d.target
	if len(d.routes) > 0 {
		hop = d.routes[0]
	}
	h, p := hostPort(hop)
	return net.JoinHostPort(h, fmt.Sprint(p))
}

func (u *ua) inDialog(d *dialog, method string, body []byte) (*msg, string, string) {
	d.cseq++
	req, br := u.request(method, d.target, d.routes, d.local, d.remote, d.callID, d.cseq, d.contact, body)
	return req, br, d.dst()
}

func (u *ua) ack(d *dialog, cseq int) {
	req, _ := u.request("ACK", d.target, d.routes, d.local, d.remote, d.callID, cseq, "", nil)
	u.sendTo(d.dst(), req)
}

// establish turns a 200 to our INVITE into a confirmed dialog and ACKs it.
func (u *ua) establish(inv, ok *msg, contact string) *dialog {
	d := &dialog{
		callID:  inv.get("Call-ID"),
		local:   inv.get("From"),
		remote:  ok.get("To"),
		target:  uriOf(ok.get("Contact")),
		routes:  reversed(ok.all("Record-Route")),
		cseq:    1,
		contact: contact,
	}
	u.ack(d, 1)
	return d
}

// hangup sends BYE in the dialog and returns how long the final answer took.
func (u *ua) hangup(d *dialog, timeout time.Duration) (time.Duration, error) {
	req, br, dst := u.inDialog(d, "BYE", nil)
	start := time.Now()
	r, err := u.transact(dst, req, br, timeout)
	if err != nil {
		return 0, err
	}
	if r.status() != 200 {
		return 0, fmt.Errorf("%s: BYE answered %s", u.name, r.first)
	}
	return time.Since(start), nil
}

// ---- the Hello node stand-in ---------------------------------------------

type binding struct {
	contact string // the registered Contact URI
	path    []string
}

type hello struct {
	*ua
	mu    sync.Mutex
	regs  map[string]*binding
	dlgs  map[string]*dialog // dialogs the phones started, by Call-ID
	byes  map[string]bool
	calls int
}

func newHello(t *testing.T, host string, port int) *hello {
	h := &hello{ua: newUA(t, "hello", host, port), regs: map[string]*binding{}, dlgs: map[string]*dialog{}, byes: map[string]bool{}}
	h.mu.Lock()
	h.onRequest = h.handle
	h.mu.Unlock()
	return h
}

func (h *hello) contactHdr() string { return "<sip:hello-sip-1@" + h.addr() + ">" }

func (h *hello) handle(m *msg) {
	switch m.method() {
	case "REGISTER":
		user := userOf(m.get("To"))
		contact := uriOf(m.get("Contact"))
		h.mu.Lock()
		h.regs[user] = &binding{contact: contact, path: m.all("Path")}
		h.mu.Unlock()
		extra := []hdr{{"Contact", "<" + contact + ">;expires=300"}}
		for _, p := range m.all("Path") {
			extra = append(extra, hdr{"Path", p})
		}
		h.reply(m, 200, "OK", "", extra, nil)
	case "INVITE":
		if tagOf(m.get("To")) != "" { // a re-INVITE from a phone
			h.reply(m, 200, "OK", "", []hdr{{"Contact", h.contactHdr()}}, h.sdp())
			return
		}
		tag := rnd(4)
		d := &dialog{
			callID:   m.get("Call-ID"),
			local:    m.get("To") + ";tag=" + tag,
			remote:   m.get("From"),
			target:   uriOf(m.get("Contact")),
			routes:   m.all("Record-Route"),
			contact:  h.contactHdr(),
			inviteRR: m.all("Record-Route"),
		}
		h.mu.Lock()
		h.dlgs[d.callID] = d
		h.calls++
		h.mu.Unlock()
		h.reply(m, 200, "OK", tag, []hdr{{"Contact", h.contactHdr()}}, h.sdp())
	case "BYE":
		h.mu.Lock()
		h.byes[m.get("Call-ID")] = true
		h.mu.Unlock()
		h.reply(m, 200, "OK", "", nil, nil)
	}
}

func userOf(v string) string {
	u := uriOf(v)
	u = strings.TrimPrefix(u, "sip:")
	user, _, _ := strings.Cut(u, "@")
	return user
}

func (h *hello) binding(user string) *binding {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.regs[user]
}

func (h *hello) dialog(callID string) *dialog {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dlgs[callID]
}

func (h *hello) ended(callID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byes[callID]
}

// callPhone is what Hello does for a call to a registered phone: an INVITE
// routed by the registration's Path.
func (h *hello) callPhone(user string) (*dialog, error) {
	b := h.binding(user)
	if b == nil {
		return nil, fmt.Errorf("no binding for %s", user)
	}
	from := "<sip:bob@" + domain + ">;tag=" + rnd(4)
	to := "<sip:" + user + "@" + domain + ">"
	inv, br := h.request("INVITE", b.contact, b.path, from, to, rnd(8)+"@hello", 1, h.contactHdr(), h.sdp())
	dst := h.addr()
	if len(b.path) > 0 {
		hh, pp := hostPort(b.path[0])
		dst = net.JoinHostPort(hh, fmt.Sprint(pp))
	}
	ok, err := h.transact(dst, inv, br, 40*time.Second)
	if err != nil {
		return nil, err
	}
	if ok.status() != 200 {
		return nil, fmt.Errorf("INVITE to %s answered %s", user, ok.first)
	}
	d := h.establish(inv, ok, h.contactHdr())
	return d, nil
}

// ---- the phone ------------------------------------------------------------

type phone struct {
	*ua
	user string

	mu        sync.Mutex
	dlgs      map[string]*dialog
	invites   chan *msg
	reinvites chan *msg
	byes      map[string]bool
}

func newPhone(t *testing.T, host, user string) *phone {
	p := &phone{ua: newUA(t, user, host, 0), user: user, dlgs: map[string]*dialog{}, invites: make(chan *msg, 8),
		reinvites: make(chan *msg, 8), byes: map[string]bool{}}
	p.mu.Lock()
	p.onRequest = p.handle
	p.mu.Unlock()
	return p
}

func (p *phone) contactURI() string { return "sip:" + p.user + "@" + p.addr() }

func (p *phone) handle(m *msg) {
	switch m.method() {
	case "INVITE":
		if tagOf(m.get("To")) != "" {
			select {
			case p.reinvites <- m:
			default:
			}
			p.reply(m, 200, "OK", "", []hdr{{"Contact", "<" + p.contactURI() + ">"}}, p.sdp())
			return
		}
		tag := rnd(4)
		d := &dialog{
			callID:  m.get("Call-ID"),
			local:   m.get("To") + ";tag=" + tag,
			remote:  m.get("From"),
			target:  uriOf(m.get("Contact")),
			routes:  m.all("Record-Route"),
			contact: "<" + p.contactURI() + ">",
		}
		p.mu.Lock()
		p.dlgs[d.callID] = d
		p.mu.Unlock()
		select {
		case p.invites <- m:
		default:
		}
		p.reply(m, 200, "OK", tag, []hdr{{"Contact", "<" + p.contactURI() + ">"}}, p.sdp())
	case "BYE":
		p.mu.Lock()
		p.byes[m.get("Call-ID")] = true
		p.mu.Unlock()
		p.reply(m, 200, "OK", "", nil, nil)
	}
}

func (p *phone) dialog(callID string) *dialog {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dlgs[callID]
}

func (p *phone) ended(callID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byes[callID]
}

// register sends REGISTER to the edge at dst and returns the 200.
func (p *phone) register(dst string) (*msg, error) {
	from := "<sip:" + p.user + "@" + domain + ">;tag=" + rnd(4)
	to := "<sip:" + p.user + "@" + domain + ">"
	req, br := p.request("REGISTER", "sip:"+domain, nil, from, to, rnd(8)+"@"+p.user, 1, "<"+p.contactURI()+">", nil)
	req.h = append(req.h, hdr{"Expires", "300"})
	r, err := p.transact(dst, req, br, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if r.status() != 200 {
		return nil, fmt.Errorf("REGISTER answered %s", r.first)
	}
	return r, nil
}

// call places a call to Hello through the edge at dst.
func (p *phone) call(dst string) (*dialog, error) {
	from := "<sip:" + p.user + "@" + domain + ">;tag=" + rnd(4)
	to := "<sip:600@" + domain + ">"
	inv, br := p.request("INVITE", "sip:600@"+domain, nil, from, to, rnd(8)+"@"+p.user, 1, "<"+p.contactURI()+">", p.sdp())
	ok, err := p.transact(dst, inv, br, 20*time.Second)
	if err != nil {
		return nil, err
	}
	if ok.status() != 200 {
		return nil, fmt.Errorf("INVITE answered %s", ok.first)
	}
	return p.establish(inv, ok, "<"+p.contactURI()+">"), nil
}

// ---- probing the VIP ------------------------------------------------------

// probe sends OPTIONS to the edge address and reports whether Kamailio
// answered 200 within wait.
func probe(dst string, wait time.Duration) bool {
	c, err := net.Dial("udp", dst)
	if err != nil {
		return false
	}
	defer func() { _ = c.Close() }()
	host := c.LocalAddr().(*net.UDPAddr)
	req := "OPTIONS sip:" + dst + " SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP " + host.String() + ";branch=z9hG4bK" + rnd(6) + ";rport\r\n" +
		"Max-Forwards: 70\r\nFrom: <sip:probe@" + domain + ">;tag=" + rnd(3) + "\r\nTo: <sip:" + dst + ">\r\n" +
		"Call-ID: " + rnd(8) + "@probe\r\nCSeq: 1 OPTIONS\r\nContent-Length: 0\r\n\r\n"
	_ = c.SetDeadline(time.Now().Add(wait))
	if _, err := c.Write([]byte(req)); err != nil {
		return false
	}
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	return err == nil && strings.HasPrefix(string(buf[:n]), "SIP/2.0 200")
}

// waitVIPAnswers polls the VIP until it answers and returns when it did.
func waitVIPAnswers(t *testing.T, dst string, d time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if probe(dst, 300*time.Millisecond) {
			return time.Now()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not answer within %s", dst, d)
	return time.Time{}
}

// ---- the test -------------------------------------------------------------

type lab struct {
	*labEnv
	hello    *hello
	vipPhone *phone
	oldPhone *phone // a phone still on its node's address
	vipAddr  string // phones' address: the VIP
	oldAddr  string // the first node's own address (the legacy sockets)
}

func hp(host string, port int) string { return net.JoinHostPort(host, fmt.Sprint(port)) }

func (l *lab) wantHop(t *testing.T, what, got, wantHost string, wantPort int) {
	t.Helper()
	h, p := hostPort(got)
	if h != wantHost || p != wantPort {
		t.Errorf("%s names %s (%q), want %s:%d", what, hp(h, p), got, wantHost, wantPort)
	}
}

// TestKamailioHA is the edge pair on kind: the flows through the VIP and
// through the node address still served for phones not yet moved, then the
// loss of the active instance (gracefully and by freezing its node), then a
// rolling restart of both.
func TestKamailioHA(t *testing.T) { runHA(t, "kw") }

// TestKamailioHAChart is the same proof for the Helm chart's pair: the chart
// rendered with values-ha.yaml and the lab's VIP, node, NIC and API server.
func TestKamailioHAChart(t *testing.T) { runHA(t, "chart") }

func runHA(t *testing.T, source string) {
	env := loadLab(t, source)
	l := &lab{labEnv: env}
	t.Cleanup(func() {
		if t.Failed() {
			l.diagnostics(t)
			if l.hello != nil {
				l.hello.dump()
			}
			for _, p := range []*phone{l.vipPhone, l.oldPhone} {
				if p != nil {
					p.dump()
				}
			}
		}
	})

	// The Hello node exists before Kamailio does, so its first probes are answered.
	l.reset(t)
	l.hello = newHello(t, l.runnerIP, l.stubPort)
	l.apply(t)
	l.waitPodsReady(t, 2, 3*time.Minute)
	first := l.waitVIP(t, time.Minute)
	holderID, _ := l.tryKubectl("-n", l.namespace, "get", "lease", "kamailio-vip", "-o", "jsonpath={.spec.holderIdentity}")
	t.Logf("VIP %s is held by %s; Lease holder: %s", l.vip, first, strings.TrimSpace(holderID))
	// The other node never holds it as well.
	if h := l.holders(); len(h) != 1 {
		t.Fatalf("VIP held by %v, want exactly one node", h)
	}

	l.vipAddr = hp(l.vip, 30508)
	l.oldAddr = hp(l.nodeIPs[l.nodes[0]], 30508)
	l.vipPhone = newPhone(t, l.runnerIP, "alice")
	l.oldPhone = newPhone(t, l.runnerIP, "carol")
	waitVIPAnswers(t, l.vipAddr, 30*time.Second)
	// Each Kamailio marks the Hello node (this test) down until a probe of its
	// own is answered: wait for one from each, or the first REGISTER gets a 503.
	for _, n := range l.nodes {
		ip := l.nodeIPs[n]
		waitFor(t, 60*time.Second, "the dispatcher on "+n+" probed the Hello node", func() bool { return l.hello.optionsFrom(ip) > 0 })
	}
	time.Sleep(time.Second)

	t.Run("vip flows", func(t *testing.T) { l.flows(t, l.vipPhone, l.vipAddr, l.vip, "VIP") })
	t.Run("legacy node address flows", func(t *testing.T) {
		l.flows(t, l.oldPhone, l.oldAddr, l.nodeIPs[l.nodes[0]], "node address")
	})
	t.Run("graceful loss of the active instance", func(t *testing.T) { l.failover(t, false) })
	t.Run("crash of the active instance", func(t *testing.T) { l.failover(t, true) })
	t.Run("rolling restart", l.rolling)
}

// flows registers a phone at edgeAddr (host:port of the phones socket) and
// runs a call each way, requiring every name Kamailio puts in the dialogs to
// be edgeHost: the VIP for phones on the VIP, the node for phones on the node.
func (l *lab) flows(t *testing.T, p *phone, edgeAddr, edgeHost, what string) {
	t.Helper()
	r, err := p.register(edgeAddr)
	if err != nil {
		t.Fatalf("register through the %s: %v", what, err)
	}
	b := l.hello.binding(p.user)
	if b == nil || len(b.path) == 0 {
		t.Fatalf("Hello saw no Path from the REGISTER through the %s (200: %s)", what, r)
	}
	// The Path (what Hello routes a call to the phone by) names the
	// Hello-facing socket on the address that survives.
	l.wantHop(t, "Path", b.path[0], edgeHost, 5070)

	// Phone to Hello.
	d, err := p.call(edgeAddr)
	if err != nil {
		t.Fatalf("call through the %s: %v", what, err)
	}
	hd := l.hello.dialog(d.callID)
	if hd == nil || len(hd.inviteRR) < 2 {
		t.Fatalf("Hello's INVITE carried Record-Route %v, want two", hd)
	}
	l.wantHop(t, "Hello-facing Record-Route", hd.inviteRR[0], edgeHost, 5070)
	l.wantHop(t, "phone-facing Record-Route", hd.inviteRR[1], edgeHost, 30508)
	if dur, err := p.hangup(d, 10*time.Second); err != nil {
		t.Fatalf("BYE from the phone: %v", err)
	} else {
		t.Logf("BYE from the phone through the %s: %s", what, dur)
	}
	waitFor(t, 10*time.Second, "Hello saw the BYE", func() bool { return l.hello.ended(d.callID) })

	// Hello to the phone, by its registration's Path, and Hello hangs up.
	hdlg, err := l.hello.callPhone(p.user)
	if err != nil {
		t.Fatalf("Hello's call to the phone through the %s: %v", what, err)
	}
	if dur, err := l.hello.hangup(hdlg, 10*time.Second); err != nil {
		t.Fatalf("BYE from Hello: %v", err)
	} else {
		t.Logf("BYE from Hello through the %s: %s", what, dur)
	}
	waitFor(t, 10*time.Second, "the phone saw the BYE", func() bool { return p.ended(hdlg.callID) })
}

func waitFor(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("not within %s: %s", d, what)
}

// failover drops the active instance (crash: kills its node, so nothing is
// released; otherwise deletes its pod, so kube-vip hands the VIP over) with
// calls up, and requires what the spec's acceptance criteria say.
func (l *lab) failover(t *testing.T, crash bool) {
	t.Helper()
	p := l.vipPhone
	if _, err := p.register(l.vipAddr); err != nil {
		t.Fatal(err)
	}

	// Calls up before the loss: c1 gets a takeover re-INVITE during the
	// outage; c2 (Hello's) is ended by the phone and c3 (the phone's) by Hello
	// after the VIP has moved.
	c1, err := p.call(l.vipAddr)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := l.hello.callPhone(p.user)
	if err != nil {
		t.Fatal(err)
	}
	c3, err := p.call(l.vipAddr)
	if err != nil {
		t.Fatal(err)
	}
	h1 := l.hello.dialog(c1.callID)
	h3 := l.hello.dialog(c3.callID)
	if h1 == nil || h3 == nil {
		t.Fatal("Hello has no dialog for the phone's calls")
	}
	c2p := p.dialog(c2.callID)
	if c2p == nil {
		t.Fatal("the phone has no dialog for Hello's call")
	}

	victim := l.waitVIP(t, 30*time.Second)
	survivor := l.nodes[0]
	if survivor == victim {
		survivor = l.nodes[1]
	}
	victimPod := l.podOn(t, victim)
	t.Logf("losing %s (pod %s); survivor %s; crash=%v", victim, victimPod, survivor, crash)

	t0 := time.Now()
	if crash {
		// A power-off: the node container is killed, its network namespace goes
		// with it (a paused one would still answer ARP for the VIP).
		run(t, "docker", "kill", victim)
		t.Cleanup(func() { _, _ = tryRun("docker", "start", victim) })
	} else {
		l.kubectl(t, "-n", l.namespace, "delete", "pod", victimPod, "--wait=false")
	}

	// The takeover's re-INVITE goes out at once, through the route set the
	// call already has (its first hop is the VIP), and is retransmitted until
	// the VIP answers again.
	type result struct {
		resp *msg
		at   time.Time
		err  error
	}
	re := make(chan result, 1)
	go func() {
		req, br, dst := l.hello.inDialog(h1, "INVITE", l.hello.sdp())
		r, err := l.hello.transact(dst, req, br, 40*time.Second)
		if err == nil && r.status() == 200 {
			l.hello.ack(h1, h1.cseq)
		}
		re <- result{r, time.Now(), err}
	}()

	// The VIP has moved when the survivor holds it and it answers, steadily
	// (the old holder may answer for a moment while it goes).
	if got := l.waitVIP(t, vipFailoverBound, victim); got != survivor {
		t.Fatalf("VIP is on %s, want the survivor %s", got, survivor)
	}
	var moved time.Time
	for streak := 0; streak < 3; {
		if probe(l.vipAddr, 300*time.Millisecond) {
			streak++
			if streak == 1 {
				moved = time.Now()
			}
		} else {
			streak = 0
		}
		if time.Since(t0) > vipFailoverBound+10*time.Second {
			t.Fatalf("the VIP does not answer steadily from %s %s after the loss", survivor, time.Since(t0))
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Logf("the VIP answers from %s %s after the loss", survivor, moved.Sub(t0).Round(100*time.Millisecond))

	// A BYE from either side ends its call within 5 s of the VIP moving.
	if dur, err := p.hangup(c2p, byeBound); err != nil {
		t.Fatalf("BYE from the phone after the failover: %v", err)
	} else {
		t.Logf("phone's BYE %s after the move", dur)
	}
	waitFor(t, 10*time.Second, "Hello saw the phone's BYE", func() bool { return l.hello.ended(c2.callID) })
	if dur, err := l.hello.hangup(h3, byeBound); err != nil {
		t.Fatalf("BYE from Hello after the failover: %v", err)
	} else {
		t.Logf("Hello's BYE %s after the move", dur)
	}
	waitFor(t, 10*time.Second, "the phone saw Hello's BYE", func() bool { return p.ended(c3.callID) })

	// The takeover re-INVITE sent during the outage completed once it was over.
	select {
	case r := <-re:
		if r.err != nil || r.resp.status() != 200 {
			t.Fatalf("the takeover re-INVITE sent during the outage: %v %v", r.resp, r.err)
		}
		// After a crash nothing answers until the VIP moves; after a graceful
		// loss the old instance may still answer for a moment.
		if crash && r.at.Before(moved.Add(-time.Second)) {
			t.Errorf("the re-INVITE completed at %s, before the VIP answered at %s", r.at.Format("15:04:05.000"), moved.Format("15:04:05.000"))
		}
		t.Logf("the takeover re-INVITE completed %s after the loss", r.at.Sub(t0).Round(100*time.Millisecond))
	case <-time.After(45 * time.Second):
		t.Fatal("the takeover re-INVITE never completed")
	}
	select {
	case <-p.reinvites:
	case <-time.After(5 * time.Second):
		t.Error("the phone never received the takeover re-INVITE")
	}
	if dur, err := p.hangup(c1, byeBound); err != nil {
		t.Fatalf("BYE of the re-INVITEd call: %v", err)
	} else {
		t.Logf("BYE of the call that was re-INVITEd during the outage: %s", dur)
	}

	// The phone registered before the failover receives a call after it,
	// with no re-REGISTER.
	d, err := l.hello.callPhone(p.user)
	if err != nil {
		t.Fatalf("an INVITE to the phone registered before the failover: %v", err)
	}
	if _, err := l.hello.hangup(d, byeBound); err != nil {
		t.Fatal(err)
	}

	// Calls on the node address still work through the survivor, where the
	// phone is on a legacy socket of that survivor only if it is the first
	// node; the VIP is what this scenario is about.
	if crash {
		run(t, "docker", "start", victim)
		if ip := strings.TrimSpace(run(t, "docker", "inspect", "-f", `{{(index .NetworkSettings.Networks "kind").IPAddress}}`, victim)); ip != l.nodeIPs[victim] {
			t.Fatalf("%s came back as %s, was %s: the lab's addresses moved", victim, ip, l.nodeIPs[victim])
		}
		// Its NAT rules went with the container.
		deadline := time.Now().Add(2 * time.Minute)
		for {
			if _, err := tryRun("docker", "exec", victim, "iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-s", l.vip+"/32", "-j", "RETURN"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not come back", victim)
			}
			time.Sleep(3 * time.Second)
		}
		l.kubectl(t, "wait", "--for=condition=Ready", "node/"+victim, "--timeout=300s")
	}
	l.waitPodsReady(t, 2, 4*time.Minute)
	l.waitStablePods(t, 2, 20*time.Second)
	// The returning instance does not take the VIP back from a healthy one.
	time.Sleep(10 * time.Second)
	if h := l.holders(); len(h) != 1 || h[0] != survivor {
		t.Errorf("after the instance returned the VIP is held by %v, want only %s", h, survivor)
	}
}

// rolling restarts both instances, one at a time as the Deployment makes it,
// with a call up and the VIP probed throughout: at least one Kamailio is Ready
// at every sample, the VIP is never dark for longer than a failover, and the
// call is still ended by a BYE afterwards.
func (l *lab) rolling(t *testing.T) {
	t.Helper()
	p := l.vipPhone
	if _, err := p.register(l.vipAddr); err != nil {
		t.Fatal(err)
	}
	cp, err := p.call(l.vipAddr)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := l.hello.callPhone(p.user)
	if err != nil {
		t.Fatal(err)
	}
	chp := p.dialog(ch.callID)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var longest time.Duration
	minReady := 2
	wg.Add(2)
	go func() { // the VIP's longest silence
		defer wg.Done()
		var darkSince time.Time
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			if probe(l.vipAddr, 150*time.Millisecond) {
				if !darkSince.IsZero() {
					mu.Lock()
					longest = max(longest, time.Since(darkSince))
					mu.Unlock()
					darkSince = time.Time{}
				}
			} else if darkSince.IsZero() {
				darkSince = time.Now()
			}
		}
	}()
	go func() { // never fewer than one Ready pod
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			if n, err := l.readyPods(); err == nil {
				mu.Lock()
				minReady = min(minReady, n)
				mu.Unlock()
				if n < 1 {
					out, _ := l.tryKubectl("-n", l.namespace, "get", "pods", "-l", "app.kubernetes.io/component=edge", "-o", "wide")
					t.Logf("%s: %d Ready:\n%s", time.Now().Format("15:04:05"), n, out)
				}
			}
		}
	}()

	l.kubectl(t, "-n", l.namespace, "rollout", "restart", "deployment/"+l.deployment)
	l.rolloutStatus(t, l.deployment, 12*time.Minute)
	l.waitPodsReady(t, 2, 2*time.Minute)
	l.waitVIP(t, 30*time.Second)
	close(stop)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("rolling restart: fewest Ready pods %d, VIP dark at most %s", minReady, longest.Round(100*time.Millisecond))
	if minReady < 1 {
		t.Errorf("a sample saw %d Ready Kamailio: a rollout must never leave both down", minReady)
	}
	if longest > vipFailoverBound {
		t.Errorf("the VIP was dark for %s during the rollout, bound %s", longest, vipFailoverBound)
	}

	waitVIPAnswers(t, l.vipAddr, 20*time.Second)
	if dur, err := p.hangup(chp, byeBound); err != nil {
		t.Fatalf("BYE of Hello's call after the rolling restart: %v", err)
	} else {
		t.Logf("phone's BYE after the rollout: %s", dur)
	}
	if dur, err := l.hello.hangup(l.hello.dialog(cp.callID), byeBound); err != nil {
		t.Fatalf("BYE of the phone's call after the rolling restart: %v", err)
	} else {
		t.Logf("Hello's BYE after the rollout: %s", dur)
	}
}
