package detect

// Shared test setup: a migrated throwaway PostgreSQL database (these tests
// skip without HELLO_TEST_DATABASE_URL), fakes for the live state and the
// cluster, and row builders. Valkey is only needed where a detector scans
// it (the register-attempt keys): HELLO_TEST_VALKEY_ADDR, database 15.

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/valkey-io/valkey-go"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func scratchDB(t testing.TB) *sql.DB {
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
	name := fmt.Sprintf("hello_detect_%d", time.Now().UnixNano())
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
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

// fakeLive is the shared live state: fixed bindings, auth failures and
// trunk statuses by trunk id.
type fakeLive struct {
	bindings []livestate.Binding
	fails    []livestate.AuthFailure
	trunks   map[int64]livestate.TrunkStatus
}

func (f *fakeLive) AllBindings(context.Context) ([]livestate.Binding, error) { return f.bindings, nil }
func (f *fakeLive) AllAuthFailures(context.Context) ([]livestate.AuthFailure, error) {
	return f.fails, nil
}
func (f *fakeLive) TrunkStatus(_ context.Context, id int64) (livestate.TrunkStatus, error) {
	return f.trunks[id], nil
}

type fakeMembers []cluster.Member

func (f fakeMembers) Members(context.Context) ([]cluster.Member, error) { return f, nil }

// testEnv is an Env over a fresh database at t0.
func testEnv(t testing.TB) (*Env, *fakeLive) {
	t.Helper()
	db := scratchDB(t)
	live := &fakeLive{trunks: map[int64]livestate.TrunkStatus{}}
	return &Env{DB: db, Store: store.New(db), Live: live, Cluster: fakeMembers{}, Now: func() time.Time { return t0 }}, live
}

// valkeyClient is a client on the CI Valkey's database 14 (flushed), or skips.
func valkeyClient(t testing.TB) valkey.Client {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 14}) // detect's DB (TestValkeyDBsPerPackage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return c
}

func exec(t testing.TB, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func queryID(t testing.TB, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(q, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return id
}

func addExtension(t testing.TB, db *sql.DB, number string) int64 {
	return queryID(t, db, `INSERT INTO extensions (number, name) VALUES ($1, $1) RETURNING id`, number)
}

func addDevice(t testing.TB, db *sql.DB, ext int64, user string, enabled bool) int64 {
	return queryID(t, db, `INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256, enabled)
		VALUES ($1, $2, 'hello.test', 'x', 'x', $3) RETURNING id`, ext, user, enabled)
}

func addTrunk(t testing.TB, db *sql.DB, name, mode string, maxCalls int, enabled bool) int64 {
	return queryID(t, db, `INSERT INTO trunks (name, mode, max_calls, enabled) VALUES ($1, $2, $3, $4) RETURNING id`, name, mode, maxCalls, enabled)
}

// cdr is one CDR row; zero fields take plausible values.
type cdr struct {
	id            int
	trunk, node   string
	start, end    time.Time
	answered      bool
	status        int
	packets, lost *int64
	jitter        *float64
}

func addCDR(t testing.TB, db *sql.DB, c cdr) {
	t.Helper()
	if c.node == "" {
		c.node = "sip-1"
	}
	if c.end.IsZero() {
		c.end = c.start.Add(30 * time.Second)
	}
	if c.status == 0 {
		c.status = 200
	}
	var answer *time.Time
	if c.answered {
		a := c.start.Add(2 * time.Second)
		answer = &a
	}
	exec(t, db, `INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, answer_time, end_time,
		duration_ms, billable_ms, sip_node, final_status, termination_side, direction, trunk_name, rtp_packets, rtp_lost, rtp_jitter_ms)
		VALUES ($1, $1, '101', '0500', $2, $3, $4, 1000, 1000, $5, $6, 'caller', 'outbound', $7, $8, $9, $10)`,
		fmt.Sprintf("c-%d-%d", c.id, c.start.UnixNano()), c.start, answer, c.end, c.node, c.status, c.trunk, c.packets, c.lost, c.jitter)
}

func ptr[T any](v T) *T { return &v }

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID + "/" + c.Severity
	}
	return out
}
