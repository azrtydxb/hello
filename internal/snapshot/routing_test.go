package snapshot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func testBox(t *testing.T, fill string) *secret.Box {
	t.Helper()
	b, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat(fill, 32))))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// seedRouting inserts two trunks (one whose password is sealed for another
// row, so it cannot open), destinations, routes and an external number.
func seedRouting(t *testing.T, conn *pgx.Conn, box *secret.Box) (good, bad int64) {
	t.Helper()
	ctx := context.Background()
	var ext int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(conn.QueryRow(ctx, "INSERT INTO extensions (number, name, external_number) VALUES ('100', 'Desk', '+97140000100') RETURNING id").Scan(&ext))
	must(conn.QueryRow(ctx, `INSERT INTO trunks (name, mode, username, realm, from_domain, register_expires, options_interval,
		source_cidrs, max_calls, default_caller_id) VALUES ('carrier-primary', 'registration', 'acct', 'carrier.test', 'from.test',
		600, 15, '{192.0.2.0/24}', 2, '+97140000000') RETURNING id`).Scan(&good))
	must(conn.QueryRow(ctx, `INSERT INTO trunks (name, mode) VALUES ('carrier-broken', 'ip') RETURNING id`).Scan(&bad))
	pw, err := box.Seal("s3cret-trunk-pass", "trunk:"+itoa(good))
	must(err)
	wrongRow, err := box.Seal("other-pass", "trunk:"+itoa(good)) // sealed for the other trunk's id
	must(err)
	_, err = conn.Exec(ctx, "UPDATE trunks SET password_enc = $1 WHERE id = $2", pw, good)
	must(err)
	_, err = conn.Exec(ctx, "UPDATE trunks SET password_enc = $1 WHERE id = $2", wrongRow, bad)
	must(err)
	_, err = conn.Exec(ctx, `INSERT INTO trunk_destinations (trunk_id, host, port, priority, weight) VALUES
		($1, '198.51.100.7', 5060, 1, 1), ($1, 'carrier.test', 0, 0, 5), ($2, '198.51.100.9', 5060, 0, 1)`, good, bad)
	must(err)
	var route int64
	must(conn.QueryRow(ctx, `INSERT INTO outbound_routes (position, name, match_kind, match, number_transform, schedule, failover_codes)
		VALUES (1, 'UAE Mobile', 'regex', '^05([0-9]{8})$', '{"prefix":"+9715","regex":"^05([0-9]{8})$","template":"+9715${1}"}',
		'{"timeZone":"Asia/Dubai","windows":[{"days":[1,2],"start":"08:00","end":"18:00"}]}', '{503}') RETURNING id`).Scan(&route))
	_, err = conn.Exec(ctx, "INSERT INTO outbound_route_trunks (route_id, trunk_id, position) VALUES ($1, $2, 1)", route, good)
	must(err)
	_, err = conn.Exec(ctx, `INSERT INTO inbound_routes (position, name, did_kind, did, trunk_id, destination_kind, destination)
		VALUES (1, 'Main DID', 'exact', '+97140000100', $1, 'extension', '100')`, good)
	must(err)
	return good, bad
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestLoadRouting fails if trunks, destinations, routes and external
// numbers are not loaded, if a password that opens is not available in
// clear to the node, or if one that does not open is dropped silently
// instead of marking its trunk misconfigured (logged without the value).
func TestLoadRouting(t *testing.T) {
	cfg := scratch(t)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	box := testBox(t, "k")
	good, bad := seedRouting(t, conn, box)

	var logs syncBuf
	w := &Watcher{Domain: domain, Box: box, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	s, err := w.loadAll(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	rs := s.Routing()
	if len(rs.Errors) > 0 {
		t.Fatalf("seed configuration does not compile: %v", rs.Errors)
	}
	c := rs.Config
	if len(c.Trunks) != 2 || c.Extensions["100"] != "+97140000100" || len(c.Outbound) != 1 || len(c.Inbound) != 1 {
		t.Fatalf("config = %+v", c)
	}
	tr := c.Trunks[0]
	if tr.ID != good || tr.Password != "s3cret-trunk-pass" || tr.RegisterExpires != 10*time.Minute || tr.OptionsInterval != 15*time.Second ||
		tr.MaxCalls != 2 || len(tr.SourceCIDRs) != 1 || tr.SourceCIDRs[0] != netip.MustParsePrefix("192.0.2.0/24") ||
		len(tr.Destinations) != 2 || tr.Destinations[0].Host != "carrier.test" || tr.FromDomain != "from.test" {
		t.Fatalf("trunk = %+v", tr)
	}
	o := c.Outbound[0]
	if o.Name != "UAE Mobile" || len(o.Trunks) != 1 || o.Trunks[0] != good || o.Number.Template != "+9715${1}" ||
		o.Schedule == nil || o.Schedule.TimeZone != "Asia/Dubai" || len(o.FailoverCodes) != 1 || o.FailoverCodes[0] != 503 {
		t.Fatalf("outbound = %+v", o)
	}
	if in := c.Inbound[0]; in.TrunkID != good || in.Destination != "100" || in.DIDKind != "exact" {
		t.Fatalf("inbound = %+v", in)
	}
	if why, ok := rs.Misconfigured[bad]; !ok || !strings.Contains(why, "does not open") {
		t.Fatalf("misconfigured = %v; want trunk %d", rs.Misconfigured, bad)
	}
	if _, ok := rs.Misconfigured[good]; ok {
		t.Fatal("good trunk marked misconfigured")
	}
	if !strings.Contains(logs.String(), "trunk misconfigured") || strings.Contains(logs.String(), "other-pass") || strings.Contains(logs.String(), "s3cret") {
		t.Fatalf("log = %s", logs.String())
	}
	if _, ok := rs.Router.Trunk(good); !ok {
		t.Fatal("router does not know the trunk")
	}

	// Without the secret key every sealed password is misconfigured.
	w2 := &Watcher{Domain: domain}
	s2, err := w2.loadAll(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Routing().Misconfigured) != 2 {
		t.Fatalf("no key: misconfigured = %v", s2.Routing().Misconfigured)
	}
}

// TestKeepLastGoodRouter fails if a revision whose routing does not
// compile replaces the last good router.
func TestKeepLastGoodRouter(t *testing.T) {
	orig := compile
	t.Cleanup(func() { compile = orig })
	var logs syncBuf
	invalid := prometheus.NewGauge(prometheus.GaugeOpts{Name: "i"})
	invalidRev := prometheus.NewGauge(prometheus.GaugeOpts{Name: "r"})
	w := &Watcher{Domain: domain, Log: slog.New(slog.NewTextHandler(&logs, nil)), ConfigInvalid: invalid, InvalidRevision: invalidRev}
	good := buildRouting(routing.Config{Extensions: map[string]string{"100": ""}}, nil, nil)
	w.install(New(1, domain, nil).WithRouting(good))

	compile = func(routing.Config) (Router, []routing.FieldError) {
		return nil, []routing.FieldError{{Path: "outbound[0].match", Message: "bad regex"}}
	}
	broken := buildRouting(routing.Config{Extensions: map[string]string{"200": ""}}, nil, nil)
	w.install(New(2, domain, nil).WithRouting(broken))
	cur := w.Current()
	if cur.Revision != 2 {
		t.Fatalf("revision = %d; devices of the new revision must still load", cur.Revision)
	}
	r := cur.Routing().Router
	if d := r.Decide(routing.Call{FromExtension: "1", Number: "100"}, nil); d.Kind != routing.KindInternal {
		t.Fatalf("last good router lost: %+v", d)
	}
	if d := r.Decide(routing.Call{FromExtension: "1", Number: "200"}, nil); d.Kind != routing.KindReject {
		t.Fatalf("broken revision's routing used: %+v", d)
	}
	if !strings.Contains(logs.String(), "does not compile") || !strings.Contains(logs.String(), "outbound[0].match: bad regex") ||
		!strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("not logged loudly: %s", logs.String())
	}
	if gauge(invalid) != 1 || gauge(invalidRev) != 2 {
		t.Fatalf("hello_routing_config_invalid = %v (revision %v), want 1 (2)", gauge(invalid), gauge(invalidRev))
	}
	// A good revision clears it.
	compile = orig
	w.install(New(3, domain, nil).WithRouting(buildRouting(routing.Config{Extensions: map[string]string{"300": ""}}, nil, nil)))
	if gauge(invalid) != 0 || gauge(invalidRev) != 0 {
		t.Fatal("invalid flag not cleared by a good revision")
	}
}

type fakeResolver struct {
	mu    sync.Mutex
	hosts map[string][]string
	srv   map[string][]*net.SRV
}

func (f *fakeResolver) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.srv["_"+service+"._"+proto+"."+name]; ok {
		return "", s, nil
	}
	return "", nil, errors.New("no SRV")
}

func (f *fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.hosts[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

// TestResolverRefreshesSourceIPs fails if destination hostnames are not
// resolved (SRV first, then A) into the router's source validation and
// the send addresses, or if a DNS change is not picked up.
func TestResolverRefreshesSourceIPs(t *testing.T) {
	r := &fakeResolver{
		hosts: map[string][]string{"sip1.carrier.test": {"203.0.113.10"}, "plain.test": {"203.0.113.20"}},
		srv:   map[string][]*net.SRV{"_sip._udp.carrier.test": {{Target: "sip1.carrier.test", Port: 5070}}},
	}
	trunks := []routing.Trunk{
		{ID: 1, Name: "a", Mode: "ip", Enabled: true, OptionsInterval: 30 * time.Second, Destinations: []routing.Destination{{Host: "carrier.test", Weight: 1}}},
		{ID: 2, Name: "b", Mode: "ip", Enabled: true, OptionsInterval: 30 * time.Second, Destinations: []routing.Destination{{Host: "plain.test", Port: 5080, Weight: 1}}},
	}
	w := &Watcher{Domain: domain, Resolver: r, ResolveInterval: 20 * time.Millisecond}
	rs := buildRouting(routing.Config{Trunks: trunks, Extensions: map[string]string{}}, nil, nil)
	if len(rs.Errors) > 0 {
		t.Fatalf("test configuration does not compile: %v", rs.Errors)
	}
	w.install(New(1, domain, nil).WithRouting(rs))
	if _, ok := w.Current().Routing().Router.TrunkForSource(netip.MustParseAddr("203.0.113.10"), ""); ok {
		t.Fatal("resolved before DNS ran")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.RunResolver(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, 3*time.Second, "SRV result used", func() bool {
		tr, ok := w.Current().Routing().Router.TrunkForSource(netip.MustParseAddr("203.0.113.10"), "")
		return ok && tr.ID == 1
	})
	rs = w.Current().Routing()
	if got := rs.Resolved["carrier.test:0"]; len(got) != 1 || got[0] != "203.0.113.10:5070" {
		t.Fatalf("resolved = %v", rs.Resolved)
	}
	if got := rs.Resolved["plain.test:5080"]; len(got) != 1 || got[0] != "203.0.113.20:5080" {
		t.Fatalf("resolved = %v", rs.Resolved)
	}
	r.mu.Lock()
	r.hosts["plain.test"] = []string{"203.0.113.21"}
	r.mu.Unlock()
	waitFor(t, 3*time.Second, "DNS change picked up", func() bool {
		tr, ok := w.Current().Routing().Router.TrunkForSource(netip.MustParseAddr("203.0.113.21"), "")
		return ok && tr.ID == 2
	})
	if w.Current().Revision != 1 {
		t.Fatal("resolution changed the revision")
	}
}

func gauge(g prometheus.Gauge) float64 {
	var m dto.Metric
	_ = g.Write(&m)
	return m.GetGauge().GetValue()
}
