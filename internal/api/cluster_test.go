package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/valkey-io/valkey-go"
)

type auditRow struct{ actor, action, resource, resourceID string }

func (e *env) audits() []auditRow {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT actor, action, resource, resource_id FROM audit_events WHERE resource = 'node' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.actor, &a.action, &a.resource, &a.resourceID); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

type clusterView struct {
	Members []struct {
		cluster.Member
		RevisionLag *int64 `json:"revisionLag"`
	} `json:"members"`
	Postgres struct {
		Up    bool   `json:"up"`
		Error string `json:"error"`
	} `json:"postgres"`
	Valkey         ValkeyHealth `json:"valkey"`
	ConfigRevision *int64       `json:"configRevision"`
}

// TestClusterAPI fails if /api/v1/cluster omits a member, its state, load,
// version or revision lag, or dependency health; if a drain or undrain is
// not audited with actor and node; if draining the last READY SIP node is
// not refused without force; or if unknown and OFFLINE nodes are not
// refused.
func TestClusterAPI(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 8}) // DBs per package: livestate 0, api 3/4/8, cluster 5, lifecycle 6, sip 7
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	ctx := context.Background()
	if err := vc.Do(ctx, vc.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	lazy := NewLazyValkey(false)
	lazy.Set(vc)
	members := cluster.New(vc)
	e := newEnvConfig(t, Config{Live: noLive{}, Cluster: lazy, Valkey: lazy}, nil)
	c := e.login()
	for _, n := range []string{"101", "102"} {
		c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": n, "name": "E" + n})
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, m := range []cluster.Member{
		{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Ready, SIPAddr: "10.0.0.11:5060", ActiveCalls: 3, Registrations: 5, Version: "v3.0.0", ConfigRevision: 2, StartedAt: now, Heartbeat: now},
		{ID: "hello-sip-2", Kind: cluster.KindSIP, State: cluster.Ready, SIPAddr: "10.0.0.12:5060", ActiveCalls: 1, Version: "v3.0.0", ConfigRevision: 1, StartedAt: now, Heartbeat: now},
		{ID: "hello-sip-3", Kind: cluster.KindSIP, State: cluster.Ready, Version: "v2.9.0", StartedAt: now, Heartbeat: now},
		{ID: "hello-control-1", Kind: cluster.KindControl, State: cluster.Ready, HTTPAddr: ":8081", Version: "v3.0.0", ConfigRevision: 2, StartedAt: now, Heartbeat: now},
	} {
		if err := members.Publish(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// hello-sip-3 stopped: only its tombstone remains.
	if err := vc.Do(ctx, vc.B().Del().Key("hello:member:hello-sip-3").Build()).Error(); err != nil {
		t.Fatal(err)
	}

	var v clusterView
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/cluster", nil).body, &v); err != nil {
		t.Fatal(err)
	}
	if !v.Postgres.Up || !v.Valkey.Up || v.Valkey.Mode != "single" || v.Valkey.Primary != "" || v.ConfigRevision == nil || *v.ConfigRevision != 2 {
		t.Fatalf("dependencies: %+v %+v revision %v", v.Postgres, v.Valkey, v.ConfigRevision)
	}
	got := map[string]string{}
	for _, m := range v.Members {
		lag := "nil"
		if m.RevisionLag != nil {
			lag = fmt.Sprint(*m.RevisionLag)
		}
		got[m.ID] = fmt.Sprintf("%s %s calls=%d regs=%d %s lag=%s", m.Kind, m.State, m.ActiveCalls, m.Registrations, m.Version, lag)
	}
	want := map[string]string{
		"hello-control-1": "control READY calls=0 regs=0 v3.0.0 lag=0",
		"hello-sip-1":     "sip READY calls=3 regs=5 v3.0.0 lag=0",
		"hello-sip-2":     "sip READY calls=1 regs=0 v3.0.0 lag=1",
		"hello-sip-3":     "sip OFFLINE calls=0 regs=0 v2.9.0 lag=2",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("members:\n got %v\nwant %v", got, want)
	}
	var nodes struct{ Items []json.RawMessage }
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/cluster/nodes", nil).body, &nodes); err != nil || len(nodes.Items) != 4 {
		t.Fatalf("nodes = %v (%v)", nodes, err)
	}

	drained := func(id string) bool {
		t.Helper()
		ok, err := members.DrainRequested(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	alice := "user:" + testUser
	c.must(http.StatusNoContent, "POST", "/api/v1/cluster/nodes/hello-sip-1/drain", nil)
	if !drained("hello-sip-1") || fmt.Sprint(e.audits()) != fmt.Sprint([]auditRow{{alice, "drain", "node", "hello-sip-1"}}) {
		t.Fatalf("drain sip-1: requested %v, audit %v", drained("hello-sip-1"), e.audits())
	}
	// hello-sip-1 acts on it; hello-sip-2 is now the last READY SIP node.
	if err := members.Publish(ctx, cluster.Member{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Draining, Version: "v3.0.0", StartedAt: now, Heartbeat: now}); err != nil {
		t.Fatal(err)
	}
	r := c.must(http.StatusConflict, "POST", "/api/v1/cluster/nodes/hello-sip-2/drain", nil)
	if !strings.Contains(string(r.body), "would leave no READY SIP node") || drained("hello-sip-2") || len(e.audits()) != 1 {
		t.Fatalf("last READY node: %s, requested %v, audits %d", r.body, drained("hello-sip-2"), len(e.audits()))
	}
	c.must(http.StatusNoContent, "POST", "/api/v1/cluster/nodes/hello-sip-2/drain?force=true", nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/cluster/nodes/hello-sip-2/drain", nil)
	c.must(http.StatusNoContent, "POST", "/api/v1/cluster/nodes/hello-control-1/drain", nil) // no SIP guard for control nodes
	if drained("hello-sip-2") || !drained("hello-control-1") {
		t.Fatal("undrain did not withdraw the request, or the control drain was not written")
	}
	wantAudit := []auditRow{
		{alice, "drain", "node", "hello-sip-1"}, {alice, "drain-force", "node", "hello-sip-2"},
		{alice, "undrain", "node", "hello-sip-2"}, {alice, "drain", "node", "hello-control-1"},
	}
	if fmt.Sprint(e.audits()) != fmt.Sprint(wantAudit) {
		t.Fatalf("audit = %v, want %v", e.audits(), wantAudit)
	}

	before := len(e.audits())
	c.must(http.StatusNotFound, "POST", "/api/v1/cluster/nodes/nope/drain", nil)
	c.must(http.StatusConflict, "DELETE", "/api/v1/cluster/nodes/nope/drain", nil)     // no request to withdraw
	c.must(http.StatusNotFound, "DELETE", "/api/v1/cluster/nodes/bad%20id/drain", nil) // not a node ID
	c.must(http.StatusNotFound, "POST", "/api/v1/cluster/nodes/bad%20id/drain", nil)
	c.must(http.StatusConflict, "POST", "/api/v1/cluster/nodes/hello-sip-3/drain", nil)
	c.must(http.StatusBadRequest, "POST", "/api/v1/cluster/nodes/hello-sip-1/drain?force=yes", nil)
	if len(e.audits()) != before || drained("hello-sip-3") {
		t.Fatal("a refused drain was audited or written")
	}

	// Valkey not connected yet: the overview reports it; the rest is 503.
	connecting := NewLazyValkey(true)
	down := newEnvConfig(t, Config{Live: connecting, Trunks: connecting, Cluster: connecting, Valkey: connecting}, nil)
	dc := down.login()
	var dv clusterView
	if err := json.Unmarshal(dc.must(http.StatusOK, "GET", "/api/v1/cluster", nil).body, &dv); err != nil {
		t.Fatal(err)
	}
	if dv.Valkey.Up || dv.Valkey.Mode != "sentinel" || dv.Valkey.Error == "" || len(dv.Members) != 0 || !dv.Postgres.Up {
		t.Fatalf("Valkey connecting: %+v", dv)
	}
	dc.must(http.StatusServiceUnavailable, "GET", "/api/v1/cluster/nodes", nil)
	dc.must(http.StatusServiceUnavailable, "POST", "/api/v1/cluster/nodes/hello-sip-1/drain", nil)
	dc.must(http.StatusServiceUnavailable, "GET", "/api/v1/registrations", nil)
}

// drainEnv is a cluster API over Valkey DB 8 with two READY SIP nodes.
func drainEnv(t *testing.T) (*env, *client, *cluster.Store, valkey.Client) {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	ctx := context.Background()
	if err := vc.Do(ctx, vc.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	lazy := NewLazyValkey(false)
	lazy.Set(vc)
	e := newEnvConfig(t, Config{Live: noLive{}, Cluster: lazy, Valkey: lazy}, nil)
	members := cluster.New(vc)
	now := time.Now().UTC()
	for _, id := range []string{"hello-sip-1", "hello-sip-2"} {
		if err := members.Publish(ctx, cluster.Member{ID: id, Kind: cluster.KindSIP, State: cluster.Ready, StartedAt: now, Heartbeat: now}); err != nil {
			t.Fatal(err)
		}
	}
	return e, e.login(), members, vc
}

// TestDrainGuardRequests fails if a SIP node with a pending drain request
// (still published READY: it acts at its next heartbeat) counts as READY,
// letting a second unforced drain empty the SIP tier, or if two concurrent
// unforced drains both pass the guard.
func TestDrainGuardRequests(t *testing.T) {
	e, c, members, _ := drainEnv(t)
	ctx := context.Background()
	c.must(http.StatusNoContent, "POST", "/api/v1/cluster/nodes/hello-sip-1/drain", nil)
	r := c.must(http.StatusConflict, "POST", "/api/v1/cluster/nodes/hello-sip-2/drain", nil)
	if !strings.Contains(string(r.body), "would leave no READY SIP node") {
		t.Fatalf("second drain: %s", r.body)
	}
	if ok, _ := members.DrainRequested(ctx, "hello-sip-2"); ok || len(e.audits()) != 1 {
		t.Fatalf("refused drain written %v or audited %v", ok, e.audits())
	}

	for round := range 10 {
		for _, id := range []string{"hello-sip-1", "hello-sip-2"} {
			if err := members.CancelDrain(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for _, id := range []string{"hello-sip-1", "hello-sip-2"} {
			wg.Go(func() { codes <- c.do("POST", "/api/v1/cluster/nodes/"+id+"/drain", nil).code })
		}
		wg.Wait()
		close(codes)
		got := map[int]int{}
		for code := range codes {
			got[code]++
		}
		if got[http.StatusNoContent] != 1 || got[http.StatusConflict] != 1 {
			t.Fatalf("round %d: concurrent drains answered %v, want one 204 and one 409", round, got)
		}
	}
}

// TestUndrainByID fails if a drain request for a node whose record and
// tombstone have expired cannot be withdrawn, or if undraining a node with
// no request (draining from SIGTERM) answers 204 and audits an undrain.
func TestUndrainByID(t *testing.T) {
	e, c, members, vc := drainEnv(t)
	ctx := context.Background()
	c.must(http.StatusNoContent, "POST", "/api/v1/cluster/nodes/hello-sip-1/drain", nil)
	// SIGKILLed while draining; its tombstone has expired since.
	for _, k := range []string{"hello:member:hello-sip-1", "hello:member:tomb:hello-sip-1"} {
		if err := vc.Do(ctx, vc.B().Del().Key(k).Build()).Error(); err != nil {
			t.Fatal(err)
		}
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/cluster/nodes/hello-sip-1/drain", nil)
	if ok, _ := members.DrainRequested(ctx, "hello-sip-1"); ok {
		t.Fatal("drain request of an expired node stayed")
	}
	// hello-sip-2 drains from SIGTERM: DRAINING with no request.
	now := time.Now().UTC()
	if err := members.Publish(ctx, cluster.Member{ID: "hello-sip-2", Kind: cluster.KindSIP, State: cluster.Draining, StartedAt: now, Heartbeat: now}); err != nil {
		t.Fatal(err)
	}
	r := c.must(http.StatusConflict, "DELETE", "/api/v1/cluster/nodes/hello-sip-2/drain", nil)
	if !strings.Contains(string(r.body), "not drained by request") {
		t.Fatalf("undrain without request: %s", r.body)
	}
	alice := "user:" + testUser
	want := []auditRow{{alice, "drain", "node", "hello-sip-1"}, {alice, "undrain", "node", "hello-sip-1"}}
	if fmt.Sprint(e.audits()) != fmt.Sprint(want) {
		t.Fatalf("audit = %v, want %v", e.audits(), want)
	}
}

// TestDrainLock fails if the drain lock can be taken twice, is not
// released by unlock, or a waiter does not give up when its context ends.
func TestDrainLock(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	lazy := NewLazyValkey(false)
	lazy.Set(vc)
	unlock, err := lazy.LockDrains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lazy.LockDrains(short); err == nil {
		t.Fatal("drain lock taken twice")
	}
	unlock()
	unlock2, err := lazy.LockDrains(context.Background())
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	unlock2()
	if _, err := NewLazyValkey(false).LockDrains(context.Background()); err == nil {
		t.Fatal("unconnected lock succeeded")
	}
}

// fakeCluster is an in-memory ClusterStore and MemberStore.
type fakeCluster struct {
	mu        sync.Mutex
	members   []cluster.Member
	drains    map[string]bool
	published []cluster.Member
	left      []string
}

func (f *fakeCluster) Members(context.Context) ([]cluster.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cluster.Member(nil), f.members...), nil
}

func (f *fakeCluster) RequestDrain(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drains[id] = true
	return nil
}

func (f *fakeCluster) CancelDrain(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.drains, id)
	return nil
}

func (f *fakeCluster) DrainRequested(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.drains[id], nil
}

func (f *fakeCluster) LockDrains(context.Context) (func(), error) { return func() {}, nil }

func (f *fakeCluster) Publish(_ context.Context, m cluster.Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, m)
	return nil
}

func (f *fakeCluster) Leave(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left = append(f.left, id)
	return nil
}

func (f *fakeCluster) setMembers(ms ...cluster.Member) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = ms
}

type upValkey struct{}

func (upValkey) ValkeyHealth(context.Context) ValkeyHealth {
	return ValkeyHealth{Up: true, Mode: "sentinel", Primary: "10.0.0.21:6379"}
}

// brokenStore accepts any bearer token, but PostgreSQL is down.
type brokenStore struct{ tokenStore }

func (brokenStore) ConfigRevision(context.Context) (int64, error) {
	return 0, errors.New("dial tcp 10.0.0.9:5432: connection refused")
}

func (brokenStore) Audit(context.Context, string, string, string, string) error {
	return errors.New("dial tcp 10.0.0.9:5432: connection refused")
}

// TestClusterDependencyFailures fails if PostgreSQL loss hides the members
// or leaks its error detail, if the Sentinel primary is not reported, or if
// a drain whose audit row cannot be written stays in effect.
func TestClusterDependencyFailures(t *testing.T) {
	fc := &fakeCluster{drains: map[string]bool{}}
	fc.setMembers(
		cluster.Member{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 4},
		cluster.Member{ID: "hello-sip-2", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 4},
	)
	h := Handler(Config{Store: brokenStore{}, Cluster: fc, Valkey: upValkey{}})
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer anything")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := do("GET", "/api/v1/cluster")
	var v clusterView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
		t.Fatalf("cluster = %d %s", rec.Code, rec.Body)
	}
	if v.Postgres.Up || v.ConfigRevision != nil || len(v.Members) != 2 || v.Members[0].RevisionLag != nil ||
		v.Valkey.Primary != "10.0.0.21:6379" || strings.Contains(rec.Body.String(), "10.0.0.9") {
		t.Fatalf("PostgreSQL down: %s", rec.Body)
	}
	if rec := do("POST", "/api/v1/cluster/nodes/hello-sip-1/drain"); rec.Code != 500 || fc.drains["hello-sip-1"] {
		t.Fatalf("unaudited drain: %d, still requested %v", rec.Code, fc.drains["hello-sip-1"])
	}
	fc.drains["hello-sip-2"] = true
	if rec := do("DELETE", "/api/v1/cluster/nodes/hello-sip-2/drain"); rec.Code != 500 || !fc.drains["hello-sip-2"] {
		t.Fatalf("unaudited undrain: %d, drain restored %v", rec.Code, fc.drains["hello-sip-2"])
	}
}

func TestSentinelPrimary(t *testing.T) {
	nodes := map[string]valkey.Client{"10.0.0.21:6379": nil}
	if got := primary(valkey.ClientModeSentinel, nodes); got != "10.0.0.21:6379" {
		t.Fatalf("sentinel primary = %q", got)
	}
	if got := primary(valkey.ClientModeStandalone, nodes); got != "" {
		t.Fatalf("single mode primary = %q", got)
	}
	if h := NewLazyValkey(false).ValkeyHealth(context.Background()); h.Up || h.Mode != "single" || h.Error == "" {
		t.Fatalf("unconnected health = %+v", h)
	}
}

// TestClusterMetrics fails if hello_cluster_members{kind,state} and
// hello_config_revision_lag{node} are not refreshed by the background
// heartbeat (no API call involved), if an OFFLINE node gets a lag, or if
// PostgreSQL loss resets the lags to a false zero.
func TestClusterMetrics(t *testing.T) {
	fc := &fakeCluster{drains: map[string]bool{}}
	fc.setMembers(
		cluster.Member{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 5},
		cluster.Member{ID: "hello-sip-2", Kind: cluster.KindSIP, State: cluster.Draining, ConfigRevision: 3},
		cluster.Member{ID: "hello-sip-3", Kind: cluster.KindSIP, State: cluster.Offline, ConfigRevision: 1},
		cluster.Member{ID: "hello-control-1", Kind: cluster.KindControl, State: cluster.Ready, ConfigRevision: 7},
	)
	var mu sync.Mutex
	rev, revErr := int64(7), error(nil)
	reg := prometheus.NewRegistry()
	m := NewClusterMetrics(reg)
	p := &ClusterPoller{
		Every: 5 * time.Millisecond, Members: fc.Members, Metrics: m,
		Revision: func(context.Context) (int64, error) { mu.Lock(); defer mu.Unlock(); return rev, revErr },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	defer func() { cancel(); <-done }()

	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("%s never held", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	gauge := func(v *prometheus.GaugeVec, labels ...string) float64 {
		var out dto.Metric
		if err := v.WithLabelValues(labels...).Write(&out); err != nil {
			t.Fatal(err)
		}
		return out.GetGauge().GetValue()
	}
	eventually("first poll", func() bool { return gauge(m.RevisionLag, "hello-sip-2") == 4 })
	if gauge(m.Members, "sip", "READY") != 1 || gauge(m.Members, "sip", "DRAINING") != 1 || gauge(m.Members, "sip", "OFFLINE") != 1 ||
		gauge(m.Members, "control", "READY") != 1 || gauge(m.RevisionLag, "hello-sip-1") != 2 || gauge(m.RevisionLag, "hello-control-1") != 0 {
		t.Fatal("members or lag gauges wrong after the first poll")
	}
	if n := series(m.RevisionLag); n != 3 {
		t.Fatalf("%d lag series, want 3 (no lag for the OFFLINE node)", n)
	}
	if p.LastRevision() != 7 {
		t.Fatalf("cached revision %d, want 7", p.LastRevision())
	}

	// PostgreSQL down: lags keep their last values.
	mu.Lock()
	revErr = errors.New("down")
	mu.Unlock()
	fc.setMembers(cluster.Member{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 6})
	eventually("members refresh", func() bool { return series(m.Members) == 1 })
	if gauge(m.RevisionLag, "hello-sip-1") != 2 {
		t.Fatal("PostgreSQL loss changed the lag")
	}
	// PostgreSQL back with revision 9: the heartbeat refreshes the lag.
	mu.Lock()
	rev, revErr = 9, nil
	mu.Unlock()
	eventually("lag refresh", func() bool {
		return gauge(m.RevisionLag, "hello-sip-1") == 3 && series(m.RevisionLag) == 1
	})
}

// series counts the label sets a vector currently exports.
func series(v *prometheus.GaugeVec) int {
	ch := make(chan prometheus.Metric, 64)
	v.Collect(ch)
	close(ch)
	return len(ch)
}

// slowConfigStore is a Store whose ConfigRevision blocks past its context's
// deadline, as a stalled PostgreSQL would.
type slowConfigStore struct{ tokenStore }

func (slowConfigStore) ConfigRevision(ctx context.Context) (int64, error) {
	<-ctx.Done()
	return 0, context.DeadlineExceeded
}

func (slowConfigStore) Audit(context.Context, string, string, string, string) error {
	return errors.New("dial tcp 10.0.0.9:5432: connection refused")
}

// TestClusterOverviewConcurrentReads fails if a slow configuration-revision
// read (a PostgreSQL-only outage) starves the Valkey health check and the
// membership read: they run concurrently, each with its own bounded
// context, so the endpoint must still report members and Valkey health.
func TestClusterOverviewConcurrentReads(t *testing.T) {
	fc := &fakeCluster{drains: map[string]bool{}}
	fc.setMembers(
		cluster.Member{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 4},
		cluster.Member{ID: "hello-sip-2", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 4},
	)
	h := Handler(Config{Store: slowConfigStore{}, Cluster: fc, Valkey: upValkey{}})
	req := httptest.NewRequest("GET", "/api/v1/cluster", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var v clusterView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
		t.Fatalf("cluster = %d %s", rec.Code, rec.Body)
	}
	if v.Postgres.Up {
		t.Fatal("a timed-out revision read must be reported as PostgreSQL down")
	}
	if len(v.Members) != 2 || v.Valkey.Primary != "10.0.0.21:6379" || !v.Valkey.Up {
		t.Fatalf("a slow revision starved the other reads: %s", rec.Body)
	}
	if v.ConfigRevision != nil {
		t.Fatal("a blocked revision read produced a revision")
	}
}

// TestClusterPollerConcurrentReads fails if a blocked configuration-revision
// read starves the membership read in the background poller: the gauges
// must still refresh (with the lags kept, not reset to a false zero).
func TestClusterPollerConcurrentReads(t *testing.T) {
	fc := &fakeCluster{drains: map[string]bool{}}
	fc.setMembers(
		cluster.Member{ID: "hello-sip-1", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 5},
		cluster.Member{ID: "hello-sip-2", Kind: cluster.KindSIP, State: cluster.Ready, ConfigRevision: 5},
	)
	var lag float64 = 2
	m := NewClusterMetrics(prometheus.NewRegistry())
	m.RevisionLag.WithLabelValues("hello-sip-1").Set(lag)
	p := &ClusterPoller{
		Every: time.Hour,
		Members: func(context.Context) ([]cluster.Member, error) {
			return fc.Members(context.Background())
		},
		Revision: func(ctx context.Context) (int64, error) {
			<-ctx.Done() // blocks past the deadline, as a stalled PostgreSQL
			return 0, context.DeadlineExceeded
		},
		Metrics: m,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.Poll(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not return after the revision read hit its deadline")
	}
	gauge := func(v *prometheus.GaugeVec, labels ...string) float64 {
		var out dto.Metric
		if err := v.WithLabelValues(labels...).Write(&out); err != nil {
			t.Fatal(err)
		}
		return out.GetGauge().GetValue()
	}
	if gauge(m.Members, "sip", "READY") != 2 {
		t.Fatal("a slow revision starved the membership gauges")
	}
	if gauge(m.RevisionLag, "hello-sip-1") != lag {
		t.Fatal("the blocked revision read reset the lag to a false zero")
	}
	if p.LastRevision() != 0 {
		t.Fatal("the blocked revision read cached a revision")
	}
}
