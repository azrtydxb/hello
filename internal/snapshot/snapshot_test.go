package snapshot

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const domain = "hello.test"

func TestNewIndexesAndSkipsForeignRealm(t *testing.T) {
	s := New(7, domain, []Device{
		{ID: 1, Username: "a", Realm: domain, Extension: "100"},
		{ID: 2, Username: "b", Realm: domain, Extension: "100"},
		{ID: 3, Username: "c", Realm: "other.test", Extension: "200"},
	})
	if s.Revision != 7 || len(s.DevicesForExtension("100")) != 2 || len(s.DevicesForExtension("200")) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	if _, ok := s.DeviceByUsername("c"); ok || len(s.Skipped) != 1 || s.Skipped[0] != "c" {
		t.Fatalf("foreign realm device kept: skipped=%v", s.Skipped)
	}
	if v := (Device{Username: "a", HA1MD5: "secret-ha1", HA1SHA256: "secret-2"}).LogValue().String(); strings.Contains(v, "secret") {
		t.Fatalf("LogValue leaks HA1: %s", v)
	}
}

// scratch creates a migrated throwaway database and returns its config.
func scratch(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_snap_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*scfg)
	defer func() { _ = db.Close() }()
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	return scfg
}

// change runs sql plus the revision bump and NOTIFY, as hello-control does.
func change(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
	var rev int64
	if err := tx.QueryRow(ctx, "UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision").Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_notify('hello_config', $1::text)", fmt.Sprint(rev)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWatcherNotifyPollAndRetention fails if the watcher is ready before the
// first load, misses a NOTIFY for more than 2s, skips a foreign realm
// silently into the snapshot, misses a change whose NOTIFY was lost (poll),
// or drops its snapshot when the database connection is lost.
func TestWatcherNotifyPollAndRetention(t *testing.T) {
	cfg := scratch(t)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var extID int64
	if err := conn.QueryRow(ctx, "INSERT INTO extensions (number, name) VALUES ('100', 'Front desk') RETURNING id").Scan(&extID); err != nil {
		t.Fatal(err)
	}

	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "f"})
	var reloads atomic.Int32
	w := &Watcher{Config: cfg, Domain: domain, PollInterval: 500 * time.Millisecond, ReloadFailures: failures,
		OnReload: func(_, _ *Snapshot) { reloads.Add(1) }}
	if w.Ready() {
		t.Fatal("ready before load")
	}
	wctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	defer func() { stop(); <-done }()
	waitFor(t, 5*time.Second, "first load", w.Ready)
	if s := w.Current(); len(s.Usernames()) != 0 {
		t.Fatalf("initial snapshot = %v", s.Usernames())
	}
	// An extension with no enabled device is still known (480, not 404).
	if s := w.Current(); !s.HasExtension("100") || s.HasExtension("999") {
		t.Fatalf("HasExtension(100)=%v HasExtension(999)=%v, want true/false", s.HasExtension("100"), s.HasExtension("999"))
	}

	start := time.Now()
	change(t, conn, `INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256, enabled) VALUES
		($1, 'desk', $2, 'm', 's', true), ($1, 'foreign', 'other.test', 'm', 's', true), ($1, 'off', $2, 'm', 's', false)`, extID, domain)
	waitFor(t, 2*time.Second, "device visible within 2s of NOTIFY", func() bool {
		_, ok := w.Current().DeviceByUsername("desk")
		return ok
	})
	t.Logf("NOTIFY to snapshot: %s", time.Since(start))
	s := w.Current()
	if d, _ := s.DeviceByUsername("desk"); d.Extension != "100" || d.ExtensionName != "Front desk" || d.HA1MD5 != "m" {
		t.Fatalf("device = %+v", d)
	}
	if _, ok := s.DeviceByUsername("foreign"); ok || len(s.Skipped) != 1 {
		t.Fatalf("foreign realm device loaded (skipped %v)", s.Skipped)
	}
	if _, ok := s.DeviceByUsername("off"); ok {
		t.Fatal("disabled device loaded")
	}

	// A change with no NOTIFY is caught by the poll.
	if _, err := conn.Exec(ctx, "UPDATE devices SET enabled = false WHERE sip_username = 'desk'; UPDATE schema_info SET config_revision = config_revision + 1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "poll catches un-notified change", func() bool {
		_, ok := w.Current().DeviceByUsername("desk")
		return !ok
	})

	// Kill the watcher's backend: it keeps the snapshot, counts, reconnects.
	rev := w.Current().Revision
	if _, err := conn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "failure counted", func() bool { return count(failures) >= 1 })
	if !w.Ready() || w.Current().Revision != rev {
		t.Fatal("snapshot lost on database error")
	}
	change(t, conn, "UPDATE extensions SET name = 'Lobby' WHERE id = $1", extID)
	waitFor(t, 5*time.Second, "reload after reconnect", func() bool { return w.Current().Revision > rev })
	if reloads.Load() < 3 {
		t.Fatalf("OnReload calls = %d", reloads.Load())
	}
}

// TestWatcherNotReadyWithoutDatabase fails if a watcher that cannot reach
// PostgreSQL reports ready.
func TestWatcherNotReadyWithoutDatabase(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "f"})
	w := &Watcher{Config: cfg, Domain: domain, ReloadFailures: failures}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	if w.Ready() || count(failures) < 1 {
		t.Fatalf("ready=%v failures=%v", w.Ready(), count(failures))
	}
}

func count(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

// TestWatcherBackoffResets fails if the reconnect delay keeps doubling
// across outages separated by healthy sessions: after five outages a
// never-reset backoff (0.3s, 0.6s, ... 4.8s) would exceed 2s.
func TestWatcherBackoffResets(t *testing.T) {
	cfg := scratch(t)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	w := &Watcher{Config: cfg, Domain: domain, InitialBackoff: 300 * time.Millisecond}
	wctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	defer func() { stop(); <-done }()
	waitFor(t, 5*time.Second, "first load", w.Ready)
	for i := range 5 {
		if _, err := conn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()"); err != nil {
			t.Fatal(err)
		}
		rev := w.Current().Revision
		start := time.Now()
		change(t, conn, "UPDATE schema_info SET created_at = created_at")
		waitFor(t, 10*time.Second, "reload after outage", func() bool { return w.Current().Revision > rev })
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("outage %d: reconnect took %s; backoff did not reset", i+1, d)
		}
	}
}

// TestExtensionAndGroupLoad fails if the Phase 4 extension features (DND,
// forwarding targets, voicemail enablement and the box the LEFT JOIN finds)
// are not loaded, if a box-less extension is not left voicemail-disabled, or
// if ring groups and feature codes are missing from the snapshot.
func TestExtensionAndGroupLoad(t *testing.T) {
	cfg := scratch(t)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `INSERT INTO extensions (number, name, dnd, forward_always, voicemail_enabled, record_default) VALUES
		('100', 'A', true, '200', true, true), ('200', 'B', false, '', true, false)`); err != nil {
		t.Fatal(err)
	}
	var a, b int64
	if err := conn.QueryRow(ctx, "SELECT id FROM extensions WHERE number = '100'").Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SELECT id FROM extensions WHERE number = '200'").Scan(&b); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO voicemail_boxes (extension_id, password_hash, email) VALUES ($1, 'h', 'b@x.test')", b); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO ring_groups (name, strategy, hunt, failure_kind, failure_target) VALUES ('700', 'ring-all', false, 'voicemail', '100')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO ring_group_members (group_id, extension_id, position, weight, delay) VALUES
		(1, $1, 2, 1, 0), (1, $2, 1, 1, 3)`, a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO feature_codes (code, action) VALUES ('*78', 'dnd_on')"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO announcements (name, minio_object) VALUES ('welcome', 'ann/welcome.wav')"); err != nil {
		t.Fatal(err)
	}

	w := &Watcher{Domain: domain}
	s, err := w.loadAll(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	ea, ok := s.Extension("100")
	if !ok || !ea.DND || ea.ForwardAlways != "200" || ea.VoicemailEnabled || ea.VoicemailBoxID != 0 {
		t.Fatalf("extension 100 = %+v ok=%v (box-less must be voicemail-disabled)", ea, ok)
	}
	if !ea.RecordDefault {
		t.Fatalf("extension 100 record_default = false, want true")
	}
	var boxB int64
	if err := conn.QueryRow(ctx, "SELECT id FROM voicemail_boxes WHERE extension_id = $1", b).Scan(&boxB); err != nil {
		t.Fatal(err)
	}
	eb, ok := s.Extension("200")
	if !ok || eb.DND || eb.ForwardAlways != "" || !eb.VoicemailEnabled || eb.VoicemailBoxID != boxB {
		t.Fatalf("extension 200 = %+v", eb)
	}
	g, ms, ok := s.RingGroup("700")
	if !ok || g.Name != "700" || g.Strategy != "ring-all" || g.FailureKind != "voicemail" || g.FailureTarget != "100" {
		t.Fatalf("ring group = %+v ok=%v", g, ok)
	}
	if len(ms) != 2 || ms[0].Extension != "200" || ms[0].Delay != 3 || ms[1].Extension != "100" || ms[1].Position != 2 {
		t.Fatalf("members = %+v (must arrive in position order)", ms)
	}
	if c, ok := s.FeatureCode("*78"); !ok || c.Action != "dnd_on" || c.Argument != "" {
		t.Fatalf("feature code = %+v ok=%v", c, ok)
	}
	if obj, ok := s.Announcement("welcome"); !ok || obj != "ann/welcome.wav" {
		t.Fatalf("announcement = %q ok=%v", obj, ok)
	}
	if _, ok := s.Announcement("missing"); ok {
		t.Fatal("unknown announcement reported present")
	}
}
