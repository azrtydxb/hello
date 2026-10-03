package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
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

// callOK places a call from a to b (both through Kamailio) and hangs up.
func callOK(t *testing.T, a, b *sipua.Phone, to string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, to, sdpOffer)
	if err != nil {
		return err
	}
	if out.Status != 200 {
		return fmt.Errorf("call = %d", out.Status)
	}
	<-got
	return out.Hangup(ctx)
}

// recover asserts that within d a new registration and a new call through
// Kamailio succeed (spec: 20s after a SIP node is lost).
func recoverWithin(t *testing.T, lc *labClient, d time.Duration) {
	t.Helper()
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := phone(t, caller, labKamailio), phone(t, callee, labKamailio)
	eventually(t, d, "new registrations and a new call through Kamailio", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, p := range []*sipua.Phone{a, b} {
			res, err := p.Register(ctx, time.Hour)
			if err != nil {
				return err
			}
			if res.StatusCode != 200 {
				return fmt.Errorf("REGISTER = %d", res.StatusCode)
			}
		}
		return callOK(t, a, b, callee.Extension)
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
	// Failed auth is keyed on the client's address, not Kamailio's.
	t.Cleanup(func() { clearThrottle(t) })
	clearThrottle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := phones[0].RegisterWithPassword(ctx, time.Hour, "wrong"); err != nil {
		t.Fatal(err)
	}
	keys := valkeyCLI(t, "--scan", "--pattern", "hello:authfail:*")
	if strings.Contains(keys, kamailioIP(t)) || strings.TrimSpace(keys) == "" {
		t.Fatalf("failed-auth keys = %q; want the client's IP, not Kamailio's (%s)", keys, kamailioIP(t))
	}
}

func TestKillSIPNodeDuringRegister(t *testing.T) {
	lc := newLabClient(t)
	d := lc.devices("desk")[0]
	p := kamPhone(t, d)
	node := lc.nodeOf(d)
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
	labCompose(t, "kill", node)
	<-done
	recoverWithin(t, lc, 20*time.Second)
}

func TestKillSIPNodeDuringRinging(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	<-rang
	var node string
	for _, c := range lc.calls() {
		if c.To == callee.Extension {
			node = c.Node
		}
	}
	if node == "" {
		t.Fatal("ringing call not listed")
	}
	t.Cleanup(func() { restore(t, lc, node) })
	labCompose(t, "kill", node)
	select {
	case <-result: // a final response or a transaction timeout: not left hanging
	case <-time.After(45 * time.Second):
		t.Fatal("caller left hanging after the node handling the ringing call died")
	}
	recoverWithin(t, lc, 20*time.Second)
	eventually(t, 40*time.Second, "dead node's call removed", func() error {
		if callListedOn(lc, node) {
			return errors.New("still listed")
		}
		return nil
	})
}

func TestKillSIPNodeDuringCall(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	<-got
	var node string
	for _, c := range lc.calls() {
		if c.To == callee.Extension {
			node = c.Node
		}
	}
	t.Cleanup(func() { restore(t, lc, node) })
	labCompose(t, "kill", node)
	recoverWithin(t, lc, 20*time.Second)
	eventually(t, 40*time.Second, "dead node's call removed", func() error {
		if callListedOn(lc, node) {
			return errors.New("still listed")
		}
		return nil
	})
}

func TestDrainKeepsCallsAndExits(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.devices("desk")[0], lc.devices("desk")[0]
	a, b := kamPhone(t, caller), kamPhone(t, callee)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	got := answerNext(ctx, b)
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	in := <-got
	var node string
	for _, c := range lc.calls() {
		if c.To == callee.Extension {
			node = c.Node
		}
	}
	t.Cleanup(func() { restore(t, lc, node) })
	lc.must("POST", "/api/v1/cluster/nodes/"+node+"/drain?force=true", nil, nil, 204)
	lc.waitState(node, "DRAINING", 10*time.Second)

	// New work goes elsewhere: wait out Kamailio's probe, then every new
	// registration lands on the other node.
	time.Sleep(15 * time.Second)
	for range 4 {
		d := lc.devices("desk")[0]
		kamPhone(t, d)
		if n := lc.nodeOf(d); n == node {
			t.Fatalf("a new registration landed on the draining node %s", node)
		}
	}
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
	other := otherNode(node)
	lc.must("POST", "/api/v1/cluster/nodes/"+other+"/drain?force=true", nil, nil, 204)
	// Cleanups run LIFO: cancel the drain first, then bring the node back.
	t.Cleanup(func() { restore(t, lc, other) }) // with no calls it drained and exited
	t.Cleanup(func() { _ = lc.do("DELETE", "/api/v1/cluster/nodes/"+other+"/drain", nil, nil, 204) })
	time.Sleep(15 * time.Second)
	c2, e2 := lc.devices("desk")[0], lc.devices("desk")[0]
	a2, b2 := kamPhone(t, c2), kamPhone(t, e2)
	lc.must("DELETE", "/api/v1/cluster/nodes/"+other+"/drain", nil, nil, 204)
	got2 := answerNext(ctx, b2)
	out2, err := a2.Dial(ctx, e2.Extension, sdpOffer)
	if err != nil || out2.Status != 200 {
		t.Fatalf("second dial = %+v, %v", out2, err)
	}
	<-got2
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
	for _, n := range []string{"hello-sip-1", "hello-sip-2"} {
		lc.waitState(n, "READY", 15*time.Second-time.Since(promoted)+5*time.Second)
	}
	recoverWithin(t, lc, 20*time.Second)
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
	time.Sleep(15 * time.Second) // Kamailio's probe drops it
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
	var withCall string
	for _, c := range lc.calls() {
		if c.To == callee.Extension {
			withCall = c.Node
		}
	}
	first := otherNode(withCall)
	t.Cleanup(func() {
		labCompose(t, "up", "-d", "--wait", "hello-sip-1", "hello-sip-2")
	})
	// Upgrade the node without the call: drain, wait for exit, start again.
	lc.must("POST", "/api/v1/cluster/nodes/"+first+"/drain?force=true", nil, nil, 204)
	eventually(t, 60*time.Second, first+" exits", func() error {
		if containerState(t, first) == "running" {
			return errors.New("still running")
		}
		return nil
	})
	restore(t, lc, first)
	recoverWithin(t, lc, 20*time.Second)
	select {
	case <-in.Ended():
		t.Fatal("the long call was dropped while the other node upgraded")
	default:
	}
	// Upgrade the node with the call: it waits for the call, which the
	// users end normally.
	lc.must("POST", "/api/v1/cluster/nodes/"+withCall+"/drain?force=true", nil, nil, 204)
	lc.waitState(withCall, "DRAINING", 10*time.Second)
	time.Sleep(15 * time.Second)
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
	recoverWithin(t, lc, 20*time.Second)
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

// valkeyPrimary asks a Sentinel which service is the primary.
func valkeyPrimary(t *testing.T) string {
	t.Helper()
	for _, s := range []string{"sentinel-1", "sentinel-2", "sentinel-3"} {
		out, err := compose("exec", "-T", s, "valkey-cli", "-p", "26379", "--json", "SENTINEL", "get-master-addr-by-name", "hello").Output()
		if err != nil {
			continue
		}
		var addr []string
		if json.Unmarshal(out, &addr) != nil || len(addr) == 0 {
			continue
		}
		for _, svc := range []string{"valkey-1", "valkey-2"} {
			if strings.HasPrefix(addr[0], svc) || addr[0] == serviceIP(t, svc) {
				return svc
			}
		}
	}
	return ""
}

func serviceIP(t *testing.T, service string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", container(t, service)).Output()
	if err != nil {
		return ""
	}
	return strings.Fields(string(out) + " ")[0]
}

func kamailioIP(t *testing.T) string { return serviceIP(t, "kamailio") }

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
