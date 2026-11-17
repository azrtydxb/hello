package store

// BenchmarkStoreDriver measures the store's statements on the two momo
// PostgreSQL drivers, to decide momo Phase 4 (switching the store from
// pgxdriver.FromSQLDB, database/sql underneath, to the native pgxpool
// driver) with numbers. Every case runs the same statement on the same
// migrated throwaway database through each driver:
//
//   - sqldb: internal/db.New over the *sql.DB, as hello-control runs today
//     (pgx stdlib; the DSN's exec mode, pgx's default caches statements).
//   - pgxpool: internal/db.NewPool over a pgxpool.Pool of the same DSN, so
//     statements go out in the pool's exec mode (statement cache), as
//     hello-sip's CDR writer runs them.
//   - pgxpool-exec: the native driver in momo's own default mode
//     (pgx.QueryExecModeExec, unnamed statements, PgBouncer-safe).
//
// The reads and the insert are the store's own code (GetCDR, ListCDRs,
// insertAudit) on a Store whose statements run on the driver under test.
// For the transaction case each driver uses its own transactions (the native
// driver cannot adopt a *sql.Tx); sqldb-adopt is the store's path today
// (database/sql BEGIN, adopted by momo, Store.tx).
//
// It needs PostgreSQL and runs only with HELLO_BENCH=1 and
// HELLO_TEST_DATABASE_URL set (CI: the dispatch-only bench job):
//
//	go test -run '^$' -bench BenchmarkStoreDriver -benchmem ./internal/store/

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/driver/pgxdriver"
	"github.com/azrtydxb/momo/momolog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/migrate"
)

// benchCDRs is how many CDRs the database holds; the page reads take 50.
const benchCDRs = 2000

type benchDriver struct {
	name   string
	m      *momo.DB
	s      *Store
	native bool
}

func BenchmarkStoreDriver(b *testing.B) {
	if os.Getenv("HELLO_BENCH") != "1" {
		b.Skip("HELLO_BENCH=1 not set")
	}
	ctx := context.Background()
	db := scratchDB(b)
	if _, err := migrate.Up(ctx, db); err != nil {
		b.Fatal(err)
	}
	seedBenchCDRs(b, db)

	// The pool connects to the scratch database scratchDB opened: reuse its
	// connection settings.
	conn, err := db.Conn(ctx)
	if err != nil {
		b.Fatal(err)
	}
	var dsn string
	if err := conn.Raw(func(dc any) error {
		dsn = dc.(interface{ Conn() *pgx.Conn }).Conn().Config().ConnString()
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	_ = conn.Close()
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		b.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	b.Logf("sqldb exec mode %v, pgxpool exec mode %v, pgxpool max conns %d",
		pcfg.ConnConfig.DefaultQueryExecMode, pool.Config().ConnConfig.DefaultQueryExecMode, pool.Config().MaxConns)

	discard := slog.New(slog.DiscardHandler)
	mk := func(name string, m *momo.DB, native bool) benchDriver {
		return benchDriver{name: name, m: m, native: native,
			s: &Store{db: &dbPool{dbConn: dbConn{m}, m: m, raw: db}}}
	}
	drivers := []benchDriver{
		mk("sqldb", hellodb.New(db, discard), false),
		mk("pgxpool", hellodb.NewPool(pool, discard), true),
		mk("pgxpool-exec", momo.New(pgxdriver.New(pool), momo.WithHook(momolog.New(discard.With("component", "db"),
			momolog.Level(slog.LevelDebug), momolog.SlowOver(hellodb.SlowQuery)))), true),
	}
	// Warm both pools and the statement caches outside the timings.
	for _, d := range drivers {
		for range 3 {
			if _, _, err := d.s.GetCDR(ctx, 1); err != nil {
				b.Fatalf("%s: warm-up: %v", d.name, err)
			}
		}
	}

	run := func(name string, fn func(b *testing.B, d benchDriver)) {
		b.Run(name, func(b *testing.B) {
			for _, d := range drivers {
				b.Run(d.name, func(b *testing.B) {
					b.ReportAllocs()
					fn(b, d)
				})
			}
		})
	}

	// A single-row read by primary key: the CDR detail (36 columns,
	// timestamptz, nullable columns and the jsonb routing trace).
	run("get-cdr", func(b *testing.B, d benchDriver) {
		var i int64
		for b.Loop() {
			i++
			if _, _, err := d.s.GetCDR(ctx, 1+i%benchCDRs); err != nil {
				b.Fatal(err)
			}
		}
	})

	// The smallest common read: one scalar.
	run("get-scalar", func(b *testing.B, d benchDriver) {
		for b.Loop() {
			if _, err := one(momo.Scalars[int64](ctx, d.m, `SELECT config_revision FROM schema_info`)); err != nil {
				b.Fatal(err)
			}
		}
	})

	// The same key read from concurrent callers (GOMAXPROCS goroutines):
	// pool checkout under contention.
	run("get-cdr-parallel", func(b *testing.B, d benchDriver) {
		b.RunParallel(func(pb *testing.PB) {
			var i int64
			for pb.Next() {
				i++
				if _, _, err := d.s.GetCDR(ctx, 1+i%benchCDRs); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})

	// A CDR list page: ListCDRs, 50 rows of 36 columns.
	run("page-cdrs-50", func(b *testing.B, d benchDriver) {
		for b.Loop() {
			out, _, err := d.s.ListCDRs(ctx, CDRFilter{}, 0, 50)
			if err != nil {
				b.Fatal(err)
			}
			if len(out) != 50 {
				b.Fatalf("got %d rows", len(out))
			}
		}
	})

	// A 50-row page with a text[] and a jsonb column. database/sql cannot
	// scan text[] into a []string, so sqldb reads it the way the store does
	// today (to_json, then json.Unmarshal); the native driver scans text[]
	// straight into []string, which is what Phase 4 would allow.
	run("page-array-jsonb-50", func(b *testing.B, d benchDriver) {
		for b.Loop() {
			if err := pageArrays(ctx, d); err != nil {
				b.Fatal(err)
			}
		}
	})

	// A single insert: the store's audit row.
	run("insert-audit", func(b *testing.B, d benchDriver) {
		for b.Loop() {
			if err := insertAudit(ctx, dbConn{d.m}, "user:bench", "update", "cdr", "1"); err != nil {
				b.Fatal(err)
			}
		}
	})

	// A short transaction of three statements (lock a row, update it, write
	// the audit row), each driver in its own transactions.
	tx3 := func(ctx context.Context, q dbq, id int64) error {
		var fr string
		if err := q.QueryRowContext(ctx, `SELECT failure_reason FROM cdrs WHERE id = @id FOR UPDATE`, momo.Named("id", id)).Scan(&fr); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE cdrs SET failure_reason = @fr WHERE id = @id`,
			momo.Named("fr", "bench "+strconv.FormatInt(id, 10)), momo.Named("id", id)); err != nil {
			return err
		}
		return insertAudit(ctx, q, "user:bench", "update", "cdr", strconv.FormatInt(id, 10))
	}
	run("tx-3-statements", func(b *testing.B, d benchDriver) {
		var i int64
		for b.Loop() {
			i++
			id := 1 + i%benchCDRs
			tx, err := d.m.Begin(ctx, momo.TxOptions{})
			if err != nil {
				b.Fatal(err)
			}
			if err := tx3(ctx, dbConn{tx}, id); err != nil {
				_ = tx.Rollback(ctx)
				b.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
	// The store's transaction path today: database/sql begins, momo adopts.
	b.Run("tx-3-statements/sqldb-adopt", func(b *testing.B) {
		b.ReportAllocs()
		s := drivers[0].s
		var i int64
		for b.Loop() {
			i++
			id := 1 + i%benchCDRs
			if err := s.tx(ctx, func(tx *dbTx) error { return tx3(ctx, tx, id) }); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// pageArrays reads 50 rows with a text[] and a jsonb column, each driver the
// way it can.
func pageArrays(ctx context.Context, d benchDriver) error {
	q := `SELECT id, start_time, trace, to_json(ARRAY[source, destination, sip_node, trunk_name]) FROM cdrs ORDER BY id DESC LIMIT 50`
	if d.native {
		q = `SELECT id, start_time, trace, ARRAY[source, destination, sip_node, trunk_name] FROM cdrs ORDER BY id DESC LIMIT 50`
	}
	rows, err := dbConn{d.m}.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var (
			id    int64
			start time.Time
			trace []byte
			arr   []string
		)
		if d.native {
			if err := rows.Scan(&id, &start, &trace, &arr); err != nil {
				return err
			}
		} else {
			var raw []byte
			if err := rows.Scan(&id, &start, &trace, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &arr); err != nil {
				return err
			}
		}
		if len(arr) != 4 || len(trace) == 0 {
			return errBenchShape
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != 50 {
		return errBenchShape
	}
	return nil
}

var errBenchShape = errors.New("store bench: unexpected result shape")

// seedBenchCDRs writes benchCDRs CDRs with a realistic routing trace.
func seedBenchCDRs(b *testing.B, db *sql.DB) {
	b.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO cdrs (correlation_id, sip_call_id, source, destination,
		start_time, ring_time, answer_time, end_time, duration_ms, billable_ms, sip_node, final_status, termination_side,
		direction, original_destination, rewritten_destination, route_name, trunk_name, rtp_packets, rtp_lost, rtp_jitter_ms,
		caller_codec, callee_codec, trace)
		SELECT 'bench-' || g, 'call-' || g || '@hello.test', '1001', '0032470' || g, s.t, s.t + interval '1 second',
			CASE WHEN g % 5 <> 0 THEN s.t + interval '3 seconds' END, s.t + interval '90 seconds', 90000, 87000,
			'sip-' || (g % 3), CASE WHEN g % 5 <> 0 THEN 200 ELSE 486 END, 'caller', 'outbound',
			'0470' || g, '+32470' || g, 'mobile', 'trunk-' || (g % 4),
			CASE WHEN g % 3 <> 0 THEN 4500 END, CASE WHEN g % 3 <> 0 THEN g % 7 END, CASE WHEN g % 3 <> 0 THEN (g % 40)::real / 4 END,
			'PCMA/8000', 'opus/48000',
			jsonb_build_array(
				jsonb_build_object('step', 'dialplan', 'input', '0470' || g, 'result', 'external'),
				jsonb_build_object('step', 'route', 'route', 'mobile', 'match', '^04', 'trunk', 'trunk-' || (g % 4)),
				jsonb_build_object('step', 'transform', 'from', '0470' || g, 'to', '+32470' || g))
		FROM generate_series(1, $1::int) g, LATERAL (SELECT now() - (g * interval '1 minute') AS t) s`, benchCDRs)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `ANALYZE`); err != nil {
		b.Fatal(err)
	}
}
