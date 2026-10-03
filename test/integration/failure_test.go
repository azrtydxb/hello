package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/sipua"
)

// Failure-injection suite (spec .procoder/specs/ha.md S-10): each test
// breaks one thing in the running lab and checks the guarantee in the
// docs/ha.md failure table. Phones reach the cluster through Kamailio.

const labKamailio = "127.0.0.1:5080"

type clusterMember struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	ActiveCalls    int    `json:"activeCalls"`
	ConfigRevision int64  `json:"configRevision"`
}

type clusterView struct {
	Members  []clusterMember `json:"members"`
	Postgres struct {
		Up bool `json:"up"`
	} `json:"postgres"`
	Valkey struct {
		Up      bool   `json:"up"`
		Mode    string `json:"mode"`
		Primary string `json:"primary"`
	} `json:"valkey"`
}

func (lc *labClient) cluster() (clusterView, error) {
	var v clusterView
	err := lc.do("GET", "/api/v1/cluster", nil, &v, 200)
	return v, err
}

// memberState returns a node's state as the cluster reports it.
func (lc *labClient) memberState(node string) string {
	v, err := lc.cluster()
	if err != nil {
		return "unknown: " + err.Error()
	}
	for _, m := range v.Members {
		if m.ID == node {
			return m.State
		}
	}
	return "absent"
}

func (lc *labClient) waitState(node, state string, within time.Duration) {
	lc.t.Helper()
	eventually(lc.t, within, node+" "+state, func() error {
		if s := lc.memberState(node); s != state {
			return fmt.Errorf("state %s", s)
		}
		return nil
	})
}

// kamPhone is a phone registered through Kamailio.
func kamPhone(t *testing.T, d labDevice) *sipua.Phone {
	t.Helper()
	p := phone(t, d, labKamailio)
	register(t, p)
	return p
}

// nodeOf returns the SIP node that holds d's binding.
func (lc *labClient) nodeOf(d labDevice) string {
	for _, b := range lc.registrations() {
		if b.Device == d.User {
			return b.ReceivedNode
		}
	}
	return ""
}

func otherNode(n string) string {
	return map[string]string{"hello-sip-1": "hello-sip-2", "hello-sip-2": "hello-sip-1"}[n]
}

// restore brings a SIP node back and waits until it is READY again.
func restore(t *testing.T, lc *labClient, node string) {
	t.Helper()
	labCompose(t, "up", "-d", "--wait", node)
	lc.waitState(node, "READY", 30*time.Second)
}

// undrain cancels a drain, ignoring a node that is not draining.
// undrain cancels a drain request; 409 is fine, because a node with no
// calls drains and exits at once, withdrawing its own request.
func undrain(lc *labClient, node string) {
	if err := lc.do("DELETE", "/api/v1/cluster/nodes/"+node+"/drain", nil, nil, 204); err != nil {
		_ = lc.do("DELETE", "/api/v1/cluster/nodes/"+node+"/drain", nil, nil, 409)
	}
}

// callOK places a call from a to b (both through Kamailio) and hangs up.
func callOK(t *testing.T, a, b *sipua.Phone, to string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return callCtx(ctx, a, b, to)
}

// callCtx is callOK bounded by ctx.
func callCtx(ctx context.Context, a, b *sipua.Phone, to string) error {
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, to, sdpOffer)
	if err != nil {
		return err
	}
	if out.Status != 200 {
		return fmt.Errorf("call = %d", out.Status)
	}
	if _, ok := <-got; !ok {
		return errors.New("callee never saw the call")
	}
	return out.Hangup(ctx)
}

// recovery checks that new registrations and a new call through Kamailio
// succeed. Its devices are provisioned up front, so the check spends its
// window on SIP, not on the API and snapshot propagation.
type recovery struct {
	t      *testing.T
	a, b   *sipua.Phone
	callee labDevice
}

func newRecovery(t *testing.T, lc *labClient) *recovery {
	t.Helper()
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	return &recovery{t: t, a: phone(t, caller, labKamailio), b: phone(t, callee, labKamailio), callee: callee}
}

// by fails the test unless a registration of both phones and a call
// between them succeed by deadline (spec: 20s after a SIP node is lost).
// Each attempt is capped to the time left.
func (r *recovery) by(deadline time.Time) {
	r.t.Helper()
	eventuallyBy(r.t, deadline, "new registrations and a new call through Kamailio", func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		for _, p := range []*sipua.Phone{r.a, r.b} {
			res, err := p.Register(ctx, time.Hour)
			if err != nil {
				return err
			}
			if res.StatusCode != 200 {
				return fmt.Errorf("REGISTER = %d", res.StatusCode)
			}
		}
		return callCtx(ctx, r.a, r.b, r.callee.Extension)
	})
}

func callListedOn(lc *labClient, node string) bool {
	for _, c := range lc.calls() {
		if c.Node == node {
			return true
		}
	}
	return false
}

// callNode waits for the call to ext to be listed and returns its node.
func callNode(t *testing.T, lc *labClient, ext string) string {
	t.Helper()
	var node string
	eventually(t, 5*time.Second, "call to "+ext+" listed", func() error {
		for _, c := range lc.calls() {
			if c.To == ext && c.Node != "" {
				node = c.Node
				return nil
			}
		}
		return errors.New("not listed")
	})
	return node
}

// goneBy waits until node lists no call, by deadline.
func goneBy(t *testing.T, lc *labClient, node string, deadline time.Time) {
	t.Helper()
	eventuallyBy(t, deadline, "dead node's call removed", func(context.Context) error {
		if callListedOn(lc, node) {
			return errors.New("still listed")
		}
		return nil
	})
}

// kill kills a SIP node and returns when it was killed.
func kill(t *testing.T, node string) time.Time {
	t.Helper()
	labCompose(t, "kill", node)
	return time.Now()
}

func TestKamailioBalancesAndPaths(t *testing.T) {
	lc := newLabClient(t)
	nodes := map[string]bool{}
	var phones []*sipua.Phone
	var devs []labDevice
	for range 8 {
		d := lc.devices("desk")[0]
		phones = append(phones, kamPhone(t, d))
		devs = append(devs, d)
		nodes[lc.nodeOf(d)] = true
	}
	if !nodes["hello-sip-1"] || !nodes["hello-sip-2"] {
		t.Fatalf("8 AORs registered through Kamailio landed on %v, want both nodes", nodes)
	}
	// Every binding records Kamailio's Hello-facing address in its Path, so
	// requests to the phone go back through Kamailio.
	bs := lc.registrations()
	for _, d := range devs {
		b := bindingsFor(bs, d.User)
		if len(b) != 1 {
			t.Fatalf("bindings for %s = %+v, want 1", d.User, b)
		}
		if !slices.ContainsFunc(b[0].Path, func(p string) bool { return strings.Contains(p, kamailioHelloAddr) }) {
			t.Fatalf("binding for %s has Path %q, want it through %s", d.User, b[0].Path, kamailioHelloAddr)
		}
	}
	// A call between phones registered on different nodes.
	var i, j = -1, -1
	for x := range devs {
		for y := range devs {
			if lc.nodeOf(devs[x]) == "hello-sip-1" && lc.nodeOf(devs[y]) == "hello-sip-2" {
				i, j = x, y
			}
		}
	}
	if err := callOK(t, phones[i], phones[j], devs[j].Extension); err != nil {
		t.Fatalf("call across nodes through Kamailio: %v", err)
	}
	// Failed auth is keyed on the client's address (the binding's source),
	// not Kamailio's.
	src := bindingsFor(bs, devs[0].User)[0].Source
	clientIP, _, err := net.SplitHostPort(src)
	if err != nil {
		t.Fatalf("binding source %q: %v", src, err)
	}
	t.Cleanup(func() { clearThrottle(t) })
	clearThrottle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := phones[0].RegisterWithPassword(ctx, time.Hour, "wrong"); err != nil {
		t.Fatal(err)
	}
	keys := strings.Fields(valkeyCLI(t, "--scan", "--pattern", "hello:authfail:*"))
	if want := "hello:authfail:" + clientIP; len(keys) != 1 || keys[0] != want {
		t.Fatalf("failed-auth keys = %q, want [%s] (the client's address)", keys, want)
	}
}

func TestKillSIPNodeDuringRegister(t *testing.T) {
	lc := newLabClient(t)
	d := lc.devices("desk")[0]
	p := kamPhone(t, d)
	node := lc.nodeOf(d)
	if node == "" {
		t.Fatal("registration not listed")
	}
	rec := newRecovery(t, lc)
	t.Cleanup(func() { restore(t, lc, node) })
	// Kill the node while REGISTERs are in flight.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = p.Register(ctx, time.Hour)
			cancel()
		}
	}()
	killed := kill(t, node)
	rec.by(killed.Add(20 * time.Second))
	<-done
}

func TestKillSIPNodeDuringRinging(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	rec := newRecovery(t, lc)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rang := make(chan struct{})
	go func() {
		if in, err := b.Next(ctx); err == nil {
			_ = in.Ring() // never answers
			close(rang)
		}
	}()
	result := make(chan error, 1)
	go func() { _, err := a.Dial(ctx, callee.Extension, sdpOffer); result <- err }()
	select {
	case <-rang:
	case <-time.After(10 * time.Second):
		t.Fatal("callee never rang")
	}
	node := callNode(t, lc, callee.Extension)
	t.Cleanup(func() { restore(t, lc, node) })
	killed := kill(t, node)
	rec.by(killed.Add(20 * time.Second))
	select {
	case <-result: // a final response or a transaction timeout: not left hanging
	case <-time.After(time.Until(killed.Add(45 * time.Second))):
		t.Fatal("caller left hanging 45s after the node handling the ringing call died")
	}
	goneBy(t, lc, node, killed.Add(40*time.Second))
}

func TestKillSIPNodeDuringCall(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	rec := newRecovery(t, lc)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	<-got
	node := callNode(t, lc, callee.Extension)
	t.Cleanup(func() { restore(t, lc, node) })
	killed := kill(t, node)
	rec.by(killed.Add(20 * time.Second))
	goneBy(t, lc, node, killed.Add(40*time.Second))
}

func TestDrainKeepsCallsAndExits(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	// A registration trunk, so the drain has a lease to hand over.
	trunk, _ := lc.trunks(0)
	eventually(t, 30*time.Second, "trunk registered", func() error {
		if st := lc.trunkStatus(trunk.ID); st.Registration == nil || st.Registration.State != "registered" {
			return fmt.Errorf("status = %+v", st.Registration)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	in := <-got
	node := callNode(t, lc, callee.Extension)
	other := otherNode(node)
	t.Logf("call on %s; trunk lease on %s", node, lc.trunkStatus(trunk.ID).Registration.Node)
	t.Cleanup(func() { restore(t, lc, node) })
	// Runs first (LIFO): a node the test left draining comes back READY.
	t.Cleanup(func() { undrain(lc, node) })
	lc.must("POST", "/api/v1/cluster/nodes/"+node+"/drain?force=true", nil, nil, 204)
	lc.waitState(node, "DRAINING", 10*time.Second)
	// The spec's 15s starts when the node starts failing (503 or gone),
	// i.e. at DRAINING, not at the API call: the node then spends a few
	// seconds finishing its drain before it exits.
	drained := time.Now()

	// New work goes elsewhere: Kamailio marks the node inactive...
	dispatcherInactiveBy(t, node, drained.Add(15*time.Second))
	// ...an INVITE sent to it directly is refused...
	direct := phone(t, lc.devices("desk")[0], nodeHostPort[node])
	if res, err := direct.Dial(ctx, callee.Extension, sdpOffer); err != nil || res.Status != 503 {
		t.Fatalf("INVITE straight to the draining node = %+v, %v; want 503", res, err)
	}
	// ...new registrations land on the other node...
	for range 4 {
		d := lc.devices("desk")[0]
		kamPhone(t, d)
		if n := lc.nodeOf(d); n != other {
			t.Fatalf("a new registration landed on %q, want %s", n, other)
		}
	}
	// ...and the trunk registration is held by the other node.
	eventually(t, 30*time.Second, "trunk lease held by "+other, func() error {
		st := lc.trunkStatus(trunk.ID)
		if st.Registration == nil || st.Registration.Node != other || st.Registration.State != "registered" {
			return fmt.Errorf("status = %+v", st.Registration)
		}
		return nil
	})
	// The draining node keeps the call and does not exit while it lasts.
	if !callListedOn(lc, node) || containerState(t, node) != "running" {
		t.Fatal("draining node dropped its call or exited early")
	}
	if err := in.Hangup(ctx); err != nil {
		t.Fatalf("hang up the drained node's call: %v", err)
	}
	eventually(t, 30*time.Second, "drained node exits after its last call", func() error {
		if s := containerState(t, node); s == "running" {
			return errors.New("still running")
		}
		return nil
	})

	// With a short drain timeout, a remaining call is hung up and the node
	// exits anyway.
	envKey := map[string]string{"hello-sip-1": "HELLO_SIP1_DRAIN_TIMEOUT", "hello-sip-2": "HELLO_SIP2_DRAIN_TIMEOUT"}[node]
	short := compose("up", "-d", "--wait", node)
	short.Env = append(short.Environ(), envKey+"=5s")
	if b, err := short.CombinedOutput(); err != nil {
		t.Fatalf("restart %s with a short drain timeout: %v\n%s", node, err, b)
	}
	lc.waitState(node, "READY", 30*time.Second)
	// Steer the next call onto that node: draining the other node first.
	lc.must("POST", "/api/v1/cluster/nodes/"+other+"/drain?force=true", nil, nil, 204)
	// Cleanups run LIFO: cancel the drain first, then bring the node back.
	t.Cleanup(func() { restore(t, lc, other) }) // with no calls it drained and exited
	t.Cleanup(func() { undrain(lc, other) })
	lc.waitState(other, "DRAINING", 10*time.Second)
	otherDrained := time.Now() // failing starts at DRAINING, as above
	dispatcherInactiveBy(t, other, otherDrained.Add(15*time.Second))
	c2, e2 := lc.devices("desk")[0], lc.devices("desk")[0]
	a2, b2 := kamPhone(t, c2), kamPhone(t, e2)
	undrain(lc, other) // 409 is fine: it may already have exited call-free
	got2 := answerNext(ctx, b2)
	out2, err := a2.Dial(ctx, e2.Extension, sdpOffer)
	if err != nil || out2.Status != 200 {
		t.Fatalf("second dial = %+v, %v", out2, err)
	}
	<-got2
	if n := callNode(t, lc, e2.Extension); n != node {
		t.Fatalf("second call on %s, want %s", n, node)
	}
	lc.must("POST", "/api/v1/cluster/nodes/"+node+"/drain?force=true", nil, nil, 204)
	select {
	case <-out2.Ended():
	case <-time.After(30 * time.Second):
		t.Fatal("drain timeout did not hang up the remaining call")
	}
	eventually(t, 30*time.Second, "node exits after the drain timeout", func() error {
		if s := containerState(t, node); s == "running" {
			return errors.New("still running")
		}
		return nil
	})
}

func TestValkeyFailover(t *testing.T) {
	lc := newLabClient(t)
	rec := newRecovery(t, lc)
	before := valkeyPrimary(t)
	t.Cleanup(func() { labCompose(t, "up", "-d", "--wait", before) })
	labCompose(t, "kill", before)
	var after string
	eventually(t, 20*time.Second, "sentinels promote the replica", func() error {
		after = valkeyPrimary(t)
		if after == before || after == "" {
			return fmt.Errorf("primary still %q", after)
		}
		return nil
	})
	promoted := time.Now()
	// Each node answers /readyz 200 within 15s of the promotion.
	for _, url := range []string{"http://localhost:8082/readyz", "http://localhost:8083/readyz"} {
		eventuallyBy(t, promoted.Add(15*time.Second), url+" ready after promotion", func(context.Context) error {
			if code := readyz(t, url); code != 200 {
				return fmt.Errorf("readyz = %d", code)
			}
			return nil
		})
	}
	// Kamailio re-probes the nodes it marked inactive while they were
	// unready, so service is measured from readiness, not promotion.
	rec.by(time.Now().Add(20 * time.Second))
	// No binding exists without its expiry on the new primary.
	for _, key := range strings.Fields(valkeyCLI(t, "--scan", "--pattern", "hello:reg:*")) {
		fields := strings.Fields(valkeyCLI(t, "HKEYS", key))
		for _, f := range fields {
			if out := strings.TrimSpace(valkeyCLI(t, "HPTTL", key, "FIELDS", "1", f)); out == "-1" {
				t.Fatalf("binding %s %s has no TTL after failover", key, f)
			}
		}
	}
}

func TestPostgresOutage(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	t.Cleanup(func() { labCompose(t, "up", "-d", "--wait") })
	labCompose(t, "stop", "postgres")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range []*sipua.Phone{a, b} {
		if res, err := p.Register(ctx, time.Hour); err != nil || res.StatusCode != 200 {
			t.Fatalf("REGISTER with PostgreSQL down = %v, %v", res, err)
		}
	}
	if err := callOK(t, a, b, callee.Extension); err != nil {
		t.Fatalf("call with PostgreSQL down: %v", err)
	}
	labCompose(t, "start", "postgres")
	lc2 := newLabClient(t) // management back once PostgreSQL is
	eventually(t, 60*time.Second, "the outage call's CDR reaches PostgreSQL", func() error {
		if len(lc2.cdrsTo(callee.Extension)) == 0 {
			return errors.New("no CDR yet")
		}
		return nil
	})
}

func TestControlPlaneRestart(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	t.Cleanup(func() { labCompose(t, "up", "-d", "--wait") })
	labCompose(t, "stop", "hello-control-1", "hello-control-2")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range []*sipua.Phone{a, b} {
		if res, err := p.Register(ctx, time.Hour); err != nil || res.StatusCode != 200 {
			t.Fatalf("REGISTER with the control plane down = %v, %v", res, err)
		}
	}
	if err := callOK(t, a, b, callee.Extension); err != nil {
		t.Fatalf("call with the control plane down: %v", err)
	}
	labCompose(t, "start", "hello-control-1", "hello-control-2")
	newLabClient(t)
}

func TestPartitionFromValkey(t *testing.T) {
	lc := newLabClient(t)
	node, ctr := "hello-sip-1", container(t, "hello-sip-1")
	network := labProject() + "_state"
	t.Cleanup(func() {
		_ = exec.Command("docker", "network", "connect", network, ctr).Run()
		lc.waitState(node, "READY", 30*time.Second)
	})
	if out, err := exec.Command("docker", "network", "disconnect", network, ctr).CombinedOutput(); err != nil {
		t.Fatalf("partition: %v\n%s", err, out)
	}
	eventually(t, 15*time.Second, "partitioned node unready", func() error {
		if code := readyz(t, "http://localhost:8082/readyz"); code != 503 {
			return fmt.Errorf("readyz = %d", code)
		}
		return nil
	})
	// Kamailio's probe sees the 503 and stops sending the node new work.
	dispatcherInactiveBy(t, node, time.Now().Add(15*time.Second))
	for range 4 {
		d := lc.devices("desk")[0]
		kamPhone(t, d)
		if n := lc.nodeOf(d); n == node {
			t.Fatalf("a new registration reached the partitioned node")
		}
	}
	if out, err := exec.Command("docker", "network", "connect", network, ctr).CombinedOutput(); err != nil {
		t.Fatalf("reconnect: %v\n%s", err, out)
	}
	eventually(t, 15*time.Second, "node ready again after reconnecting", func() error {
		if code := readyz(t, "http://localhost:8082/readyz"); code != 200 {
			return fmt.Errorf("readyz = %d", code)
		}
		return nil
	})
}

func TestRollingUpgrade(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	in := <-got
	withCall := callNode(t, lc, callee.Extension)
	first := otherNode(withCall)
	// Cleanups run LIFO: cancel any drain the test left, then bring both
	// nodes back.
	t.Cleanup(func() {
		labCompose(t, "up", "-d", "--wait", "hello-sip-1", "hello-sip-2")
	})
	t.Cleanup(func() {
		undrain(lc, "hello-sip-1")
		undrain(lc, "hello-sip-2")
	})
	// Upgrade the node without the call: drain, wait for exit, start again.
	// A new call during the drain goes to the other node.
	rec := newRecovery(t, lc)
	drained := time.Now()
	lc.must("POST", "/api/v1/cluster/nodes/"+first+"/drain?force=true", nil, nil, 204)
	rec.by(drained.Add(20 * time.Second))
	eventually(t, 60*time.Second, first+" exits", func() error {
		if containerState(t, first) == "running" {
			return errors.New("still running")
		}
		return nil
	})
	restore(t, lc, first)
	newRecovery(t, lc).by(time.Now().Add(20 * time.Second))
	select {
	case <-in.Ended():
		t.Fatal("the long call was dropped while the other node upgraded")
	default:
	}
	// Upgrade the node with the call: it waits for the call, which the
	// users end normally. A new call during the drain goes to the
	// upgraded node.
	rec = newRecovery(t, lc)
	drained = time.Now()
	lc.must("POST", "/api/v1/cluster/nodes/"+withCall+"/drain?force=true", nil, nil, 204)
	lc.waitState(withCall, "DRAINING", 10*time.Second)
	rec.by(drained.Add(20 * time.Second))
	select {
	case <-in.Ended():
		t.Fatal("draining dropped the long call")
	default:
	}
	if err := out.Hangup(ctx); err != nil {
		t.Fatalf("hang up the long call: %v", err)
	}
	eventually(t, 30*time.Second, withCall+" exits after the call", func() error {
		if containerState(t, withCall) == "running" {
			return errors.New("still running")
		}
		return nil
	})
	restore(t, lc, withCall)
	newRecovery(t, lc).by(time.Now().Add(20 * time.Second))
}

// ---- lab plumbing ----

func container(t *testing.T, service string) string {
	t.Helper()
	out, err := compose("ps", "-a", "-q", service).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("container for %s: %v", service, err)
	}
	return strings.TrimSpace(string(out))
}

func containerState(t *testing.T, service string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Status}}", container(t, service)).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func readyz(t *testing.T, url string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// valkeyPrimary asks a Sentinel which service is the primary. Sentinels
// run with announce-hostnames and the servers announce their service names,
// so the reply is a name; the next Sentinel is asked only on an error.
func valkeyPrimary(t *testing.T) string {
	t.Helper()
	for _, s := range []string{"sentinel-1", "sentinel-2", "sentinel-3"} {
		out, err := compose("exec", "-T", s, "valkey-cli", "-p", "26379", "--json", "SENTINEL", "get-master-addr-by-name", "hello").Output()
		if err != nil {
			continue
		}
		var addr []string
		if err := json.Unmarshal(out, &addr); err != nil || len(addr) == 0 {
			t.Logf("%s: get-master-addr-by-name = %q", s, out)
			return ""
		}
		for _, svc := range []string{"valkey-1", "valkey-2"} {
			if addr[0] == svc {
				return svc
			}
		}
		return ""
	}
	return ""
}

// Kamailio's address on the network it shares with the SIP nodes: the Path
// it adds to REGISTERs and the source of everything it relays.
const kamailioHelloAddr = "10.89.53.10:5070"

// nodeIP is each SIP node's fixed address, as the dispatcher list names it.
var nodeIP = map[string]string{"hello-sip-1": "10.89.53.11", "hello-sip-2": "10.89.53.12"}

// nodeHostPort is each SIP node's SIP port published on the host.
var nodeHostPort = map[string]string{"hello-sip-1": labSIP1, "hello-sip-2": labSIP2}

// dispatcherFlags returns the flags Kamailio's dispatcher holds for node
// (e.g. "AP" active and probing, "IP" inactive).
func dispatcherFlags(node string) (string, error) {
	out, err := compose("exec", "-T", "kamailio", "kamcmd", "dispatcher.list").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kamcmd dispatcher.list: %w\n%s", err, out)
	}
	return parseDispatcherFlags(string(out), nodeIP[node])
}

// parseDispatcherFlags finds the destination sip:<ip>:... in kamcmd
// dispatcher.list output and returns the FLAGS that follow its URI.
func parseDispatcherFlags(list, ip string) (string, error) {
	found := false
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if uri, ok := strings.CutPrefix(line, "URI:"); ok {
			uri = strings.TrimSpace(uri)
			found = uri == "sip:"+ip || strings.HasPrefix(uri, "sip:"+ip+":") || strings.HasPrefix(uri, "sip:"+ip+";")
			continue
		}
		if flags, ok := strings.CutPrefix(line, "FLAGS:"); ok && found {
			return strings.TrimSpace(flags), nil
		}
	}
	return "", fmt.Errorf("no dispatcher destination for %s in:\n%s", ip, list)
}

// dispatcherInactiveBy fails the test unless Kamailio marks node inactive
// (flags starting with I) by deadline.
func dispatcherInactiveBy(t *testing.T, node string, deadline time.Time) {
	t.Helper()
	eventuallyBy(t, deadline, "Kamailio marks "+node+" inactive", func(context.Context) error {
		flags, err := dispatcherFlags(node)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(flags, "I") {
			return fmt.Errorf("flags %s", flags)
		}
		return nil
	})
}

// valkeyCLI runs valkey-cli against the current primary.
func valkeyCLI(t *testing.T, args ...string) string {
	t.Helper()
	p := valkeyPrimary(t)
	if p == "" {
		t.Fatal("no Valkey primary")
	}
	out, err := compose(append([]string{"exec", "-T", p, "valkey-cli"}, args...)...).Output()
	if err != nil {
		t.Fatalf("valkey-cli %v: %v", args, err)
	}
	return string(out)
}

func clearThrottle(t *testing.T) {
	t.Helper()
	for _, k := range strings.Fields(valkeyCLI(t, "--scan", "--pattern", "hello:authfail:*")) {
		valkeyCLI(t, "DEL", k)
	}
}

func TestParseDispatcherFlags(t *testing.T) {
	list := `{
	NRSETS: 1
	RECORDS: {
		SET: {
			ID: 1
			TARGETS: {
				DEST: {
					URI: sip:10.89.53.11:5060
					FLAGS: AP
					PRIORITY: 0
				}
				DEST: {
					URI: sip:10.89.53.12:5060
					FLAGS: IP
					PRIORITY: 0
				}
			}
		}
	}
}`
	for ip, want := range map[string]string{"10.89.53.11": "AP", "10.89.53.12": "IP"} {
		if got, err := parseDispatcherFlags(list, ip); err != nil || got != want {
			t.Errorf("flags for %s = %q, %v; want %s", ip, got, err, want)
		}
	}
	if _, err := parseDispatcherFlags(list, "10.89.53.1"); err == nil {
		t.Error("an IP that is only a prefix of a destination matched")
	}
}
