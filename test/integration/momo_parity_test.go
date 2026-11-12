package integration

// Phase 0 of the momo adoption plan: prove that momo v0.8.0 can sit on top of
// the goose-managed schema and the existing pgx pool without any change to
// production code. Nothing here is imported by a production package.
//
// What is proved, each as its own subtest so one run reports every finding:
//
//   - read and write through momo.Exec/Query over pgxdriver.FromSQLDB(db),
//     with Hello's error mapping (pgconn.PgError) still reachable;
//   - AdoptTx(SQLTx(tx)): momo and raw database/sql share one transaction,
//     momo never finishes an adopted transaction, UnwrapSQLTx hands the same
//     transaction back to legacy code;
//   - advisory.XactLock contends with the raw pg_advisory_xact_lock key the
//     config lock uses, so old and new replicas serialise on one key;
//   - pgxdriver.Listen/Notify (upstream #102, shipped in v0.8.0);
//   - baseline: generate --from-db's library path run against the goose-built
//     schema, then Baseline and Drift report no difference;
//   - the init migration built from that snapshot yields a schema equal to the
//     goose-built one.
//
// The tests skip without HELLO_TEST_DATABASE_URL (the integration job sets it).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/advisory"
	"github.com/azrtydxb/momo/driver/pgxdriver"
	"github.com/azrtydxb/momo/gen/fromdb"
	momomigrate "github.com/azrtydxb/momo/migrate"
	momopg "github.com/azrtydxb/momo/migrate/postgres"

	"github.com/azrtydxb/hello/internal/migrate"
)

// legacyHistory is goose's table and its sequence: they are not part of the
// application schema and momo must not model or report them.
var legacyHistory = []string{"goose_db_version", "goose_db_version_*"}

// momoRig is a goose-migrated scratch database with a momo handle on the same
// *sql.DB that production code would use.
type momoRig struct {
	dsn string
	db  *sql.DB
	drv momo.Driver
	m   *momo.DB
}

func newMomoRig(t *testing.T) *momoRig {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	dsn = scratchDatabase(t, ctx, dsn)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	drv := pgxdriver.FromSQLDB(db)
	return &momoRig{dsn: dsn, db: db, drv: drv, m: momo.New(drv)}
}

type trunkRow struct {
	ID              int64
	Name            string
	Mode            string
	RegisterExpires int32
	MaxCalls        int32
}

const insertTrunk = `INSERT INTO trunks (name, mode, max_calls) VALUES (@name, @mode, @max) RETURNING id`

func TestMomoParityReadWrite(t *testing.T) {
	r := newMomoRig(t)
	ctx := context.Background()

	ids, err := momo.Scalars[int64](ctx, r.m, insertTrunk,
		momo.Named("name", "momo-a"), momo.Named("mode", "ip"), momo.Named("max", 7))
	if err != nil || len(ids) != 1 {
		t.Fatalf("insert through momo: ids=%v err=%v", ids, err)
	}

	// The row is visible to plain database/sql on the same pool, and momo
	// scans it into a plain struct with no generated metadata.
	var rawName string
	if err := r.db.QueryRowContext(ctx, `SELECT name FROM trunks WHERE id = $1`, ids[0]).Scan(&rawName); err != nil || rawName != "momo-a" {
		t.Fatalf("raw read of momo write: %q, %v", rawName, err)
	}
	rows, err := momo.Query[trunkRow](ctx, r.m,
		`SELECT id, name, mode, register_expires, max_calls FROM trunks WHERE id = @id`, momo.Named("id", ids[0]))
	if err != nil || len(rows) != 1 {
		t.Fatalf("momo read: rows=%v err=%v", rows, err)
	}
	if got := rows[0]; got.Name != "momo-a" || got.Mode != "ip" || got.MaxCalls != 7 || got.RegisterExpires != 3600 {
		t.Fatalf("momo read scanned %+v; want name momo-a, mode ip, max_calls 7, register_expires 3600 (the column default)", got)
	}

	// An array column survives the database/sql scan path as text.
	if _, err := momo.Exec(ctx, r.m, `UPDATE trunks SET source_cidrs = '{10.0.0.0/8,192.168.1.0/24}'::cidr[] WHERE id = @id`, momo.Named("id", ids[0])); err != nil {
		t.Fatalf("update cidr[]: %v", err)
	}
	cidrs, err := momo.Scalars[string](ctx, r.m, `SELECT source_cidrs::text FROM trunks WHERE id = @id`, momo.Named("id", ids[0]))
	if err != nil || len(cidrs) != 1 || cidrs[0] != "{10.0.0.0/8,192.168.1.0/24}" {
		t.Fatalf("cidr[] read: %v, %v", cidrs, err)
	}

	// store.mapErr keys on *pgconn.PgError (23505, 23503 and the message of
	// the FK case): both the momo kind and the pgconn cause must be
	// reachable, or mapErr would stop recognising conflicts.
	_, err = momo.Scalars[int64](ctx, r.m, insertTrunk,
		momo.Named("name", "momo-a"), momo.Named("mode", "ip"), momo.Named("max", 0))
	if !errors.Is(err, momo.ErrUniqueViolation) {
		t.Fatalf("duplicate name: err = %v; want momo.ErrUniqueViolation", err)
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" {
		t.Fatalf("duplicate name: pgconn.PgError not reachable through momo error (%v)", err)
	}
	_, err = momo.Scalars[int64](ctx, r.m, insertTrunk,
		momo.Named("name", "momo-b"), momo.Named("mode", "bogus"), momo.Named("max", 0))
	if !errors.Is(err, momo.ErrCheckViolation) {
		t.Fatalf("bad mode: err = %v; want momo.ErrCheckViolation", err)
	}
	if _, err := momo.Exec(ctx, r.m, `INSERT INTO inbound_routes (position, name, did_kind, trunk_id) VALUES (1, 'x', 'any', @t)`, momo.Named("t", int64(987654321))); !errors.Is(err, momo.ErrForeignKeyViolation) {
		t.Fatalf("dangling trunk_id: err = %v; want momo.ErrForeignKeyViolation", err)
	} else if !errors.As(err, &pg) || pg.Code != "23503" {
		t.Fatalf("foreign key: pgconn.PgError not reachable (%v)", err)
	}
}

func TestMomoParityAdoptTx(t *testing.T) {
	r := newMomoRig(t)
	ctx := context.Background()

	count := func(q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) int {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM trunks WHERE name LIKE 'adopt-%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("rollback", func(t *testing.T) {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		mtx := r.m.AdoptTx(pgxdriver.SQLTx(tx))
		if _, err := momo.Exec(ctx, mtx, `INSERT INTO trunks (name, mode) VALUES (@n, 'ip')`, momo.Named("n", "adopt-rb")); err != nil {
			t.Fatalf("momo insert in adopted tx: %v", err)
		}
		if n := count(tx); n != 1 {
			t.Fatalf("raw statement in the same tx sees %d rows, want 1", n)
		}
		if n := count(r.db); n != 0 {
			t.Fatalf("another connection sees %d uncommitted rows", n)
		}
		if err := mtx.Commit(ctx); !errors.Is(err, momo.ErrAdoptedTx) {
			t.Fatalf("momo Commit on an adopted tx = %v; want ErrAdoptedTx", err)
		}
		if err := mtx.Rollback(ctx); !errors.Is(err, momo.ErrAdoptedTx) {
			t.Fatalf("momo Rollback on an adopted tx = %v; want ErrAdoptedTx", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if n := count(r.db); n != 0 {
			t.Fatalf("rolled-back adopted tx left %d rows", n)
		}
	})

	t.Run("commit_by_owner", func(t *testing.T) {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		mtx := r.m.AdoptTx(pgxdriver.SQLTx(tx))
		if _, err := momo.Exec(ctx, mtx, `INSERT INTO trunks (name, mode) VALUES (@n, 'ip')`, momo.Named("n", "adopt-ok")); err != nil {
			t.Fatal(err)
		}
		if got, ok := pgxdriver.UnwrapSQLTx(mtx); !ok || got != tx {
			t.Fatalf("UnwrapSQLTx = %p, %v; want the adopted *sql.Tx %p", got, ok, tx)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := count(r.db); n != 1 {
			t.Fatalf("committed adopted tx: %d rows, want 1", n)
		}
		if _, err := momo.Exec(ctx, mtx, `SELECT 1`); err == nil {
			t.Fatal("statement on a finished adopted tx succeeded")
		}
	})

	t.Run("momo_owned_tx_shares_with_legacy_code", func(t *testing.T) {
		err := r.m.Tx(ctx, momo.TxOptions{}, func(mtx *momo.Tx) error {
			stx, ok := pgxdriver.UnwrapSQLTx(mtx)
			if !ok {
				return errors.New("UnwrapSQLTx failed on a momo-begun tx over FromSQLDB")
			}
			if _, err := stx.ExecContext(ctx, `INSERT INTO trunks (name, mode) VALUES ('adopt-legacy', 'ip')`); err != nil {
				return err
			}
			if _, err := momo.Exec(ctx, mtx, `INSERT INTO trunks (name, mode) VALUES ('adopt-momo', 'ip')`); err != nil {
				return err
			}
			return errors.New("force rollback")
		})
		if err == nil || err.Error() != "force rollback" {
			t.Fatalf("Tx = %v; want the callback's error", err)
		}
		var n int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM trunks WHERE name IN ('adopt-legacy','adopt-momo')`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("rolled-back mixed tx left %d rows (%v)", n, err)
		}
	})
}

// TestMomoParityConfigLock: the config lock key stays the one old pods take as
// a raw int64, so a momo-taking replica and a goose-era replica serialise.
func TestMomoParityConfigLock(t *testing.T) {
	r := newMomoRig(t)
	ctx := context.Background()
	const configLockKey = 0x68656c6c6f636667 // internal/store/store.go

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	mtx := r.m.AdoptTx(pgxdriver.SQLTx(tx))
	if err := advisory.XactLock(ctx, mtx, int64(configLockKey)); err != nil {
		t.Fatalf("advisory.XactLock on an adopted tx: %v", err)
	}
	var got bool
	if err := r.db.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, int64(configLockKey)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("a raw replica took the config lock while momo held it: the keys differ")
	}
}

func TestMomoParityListenNotify(t *testing.T) {
	r := newMomoRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := pgx.ParseConfig(r.dsn)
	if err != nil {
		t.Fatal(err)
	}
	l, err := pgxdriver.Listen(ctx, cfg, []string{"hello_momo_probe"})
	if err != nil {
		t.Fatalf("pgxdriver.Listen: %v", err)
	}
	defer func() { _ = l.Close(context.Background()) }()

	// A rolled-back NOTIFY is never delivered; a committed one is, on commit.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	mtx := r.m.AdoptTx(pgxdriver.SQLTx(tx))
	if err := pgxdriver.Notify(ctx, mtx, "hello_momo_probe", "rolled-back"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := pgxdriver.Notify(ctx, r.m, "hello_momo_probe", "delivered"); err != nil {
		t.Fatal(err)
	}
	n, err := l.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if n.Channel != "hello_momo_probe" || n.Payload != "delivered" {
		t.Fatalf("first notification = %+v; want the committed one (the rolled-back one must not arrive)", n)
	}
}

// parityConfig is the introspect section the plan proposes for Hello.
func parityConfig() fromdb.Config {
	return fromdb.Config{
		Package:   "models",
		UUID:      fromdb.UUIDGoogle,
		DBDefault: "all",
		Types: map[string]fromdb.TypeRule{
			"inet":   {Go: "string"},
			"cidr":   {Go: "string"},
			"inet[]": {Go: "[]string"},
			"cidr[]": {Go: "[]string"},
		},
	}
}

func parityEngine(t *testing.T, r *momoRig, dir fs.FS) *momomigrate.Engine {
	t.Helper()
	e, err := momomigrate.NewEngine(r.drv, dir,
		momomigrate.WithDialect(momopg.New()),
		momomigrate.WithConfig(momomigrate.Config{Exclude: legacyHistory, LockTimeout: 30 * time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// initMigration derives the init migration and snapshot from the live goose
// schema, exactly as `momo generate --from-db` does, and returns it as a
// migration directory.
func initMigration(t *testing.T, r *momoRig) (fstest.MapFS, string, *fromdb.Result) {
	t.Helper()
	ctx := context.Background()
	empty := parityEngine(t, r, fstest.MapFS{})
	live, err := empty.Inspect(ctx)
	if err != nil {
		t.Fatalf("Inspect the goose schema: %v", err)
	}
	t.Logf("inspected %d tables, %d functions, %d triggers", len(live.Tables), len(live.Functions), len(live.Triggers))
	res, err := fromdb.Generate(live, fromdb.Options{Package: "models", Config: parityConfig()})
	if err != nil {
		t.Fatalf("fromdb.Generate: %v", err)
	}
	for _, f := range res.Findings {
		t.Logf("from-db finding: %s", f)
	}
	if res.Failed() {
		t.Fatal("fromdb.Generate reported errors; the models cannot be written (see the findings above)")
	}
	mig, err := empty.Diff(ctx, res.Snapshot, "init", momomigrate.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff to the init migration: %v", err)
	}
	dir := fstest.MapFS{}
	var size int
	for name, content := range mig.Files {
		dir[name] = &fstest.MapFile{Data: content}
		size += len(content)
		t.Logf("init file %s: %d bytes", name, len(content))
	}
	t.Logf("init migration %s: %d files, %d bytes (the snapshot is what grows per later migration)", mig.Version, len(mig.Files), size)
	return dir, mig.Version, res
}

func TestMomoParityBaseline(t *testing.T) {
	r := newMomoRig(t)
	ctx := context.Background()
	dir, version, _ := initMigration(t, r)
	e := parityEngine(t, r, dir)

	// Baseline checks the live database against the snapshot and records the
	// init migration as applied without running any DDL.
	if err := e.Baseline(ctx, version, false); err != nil {
		t.Fatalf("Baseline on the goose-built schema: %v", err)
	}
	st, err := e.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 1 {
		t.Fatalf("Status after baseline: %+v; want one applied migration", st)
	}
	if rep, err := e.Apply(ctx, momomigrate.ApplyOptions{}); err != nil || len(rep.Applied) != 0 {
		t.Fatalf("Apply after baseline applied %v (%v); want nothing", rep, err)
	}

	d, err := e.DriftWith(ctx, momomigrate.DriftOptions{Strict: true})
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	for _, o := range d.Changed {
		t.Errorf("drift changed: %s %s: %s", o.Object.Kind, o.Object.Name, o.Detail)
	}
	for _, o := range d.Missing {
		t.Errorf("drift missing: %s %s", o.Kind, o.Name)
	}
	for _, o := range d.Unmanaged {
		t.Errorf("drift unmanaged: %s %s", o.Kind, o.Name)
	}
	for _, o := range d.CanonicalizeSkipped {
		t.Logf("canonicalize skipped: %s %s", o.Kind, o.Name)
	}

	// goose's history is untouched, so an older image still sees no pending
	// goose work after the takeover.
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("goose_db_version after baseline: %d rows, %v", n, err)
	}
	if got, err := migrate.Up(ctx, r.db); err != nil || got != 0 {
		t.Fatalf("goose Up after baseline = %d, %v; want 0, nil", got, err)
	}
}

func TestMomoParityInitEqualsGoose(t *testing.T) {
	goose := newMomoRig(t)
	ctx := context.Background()
	dir, _, _ := initMigration(t, goose)

	// A second database built only from the init migration.
	fresh := newMomoRig(t)
	if _, err := fresh.db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	fe := parityEngine(t, fresh, dir)
	rep, err := fe.Apply(ctx, momomigrate.ApplyOptions{LockWait: time.Minute})
	if err != nil {
		t.Fatalf("Apply the init migration to an empty database: %v", err)
	}
	t.Logf("init migration applied: %d migration(s)", len(rep.Applied))

	exclude := append([]string{"momo_migrations*"}, legacyHistory...)
	inspect := func(r *momoRig) *schemaJSON {
		e, err := momomigrate.NewEngine(r.drv, fstest.MapFS{}, momomigrate.WithDialect(momopg.New()),
			momomigrate.WithConfig(momomigrate.Config{Exclude: exclude}))
		if err != nil {
			t.Fatal(err)
		}
		s, err := e.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return toSchemaJSON(t, s)
	}
	a, b := inspect(goose), inspect(fresh)
	for _, d := range a.diff(b) {
		t.Errorf("goose-built vs init-built: %s", d)
	}
}

// schemaJSON is an Inspect result keyed for comparison: each top-level
// collection by object name, each object as canonical JSON.
type schemaJSON struct{ parts map[string]string }

func toSchemaJSON(t *testing.T, s any) *schemaJSON {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	out := &schemaJSON{parts: map[string]string{}}
	for coll, v := range top {
		var list []json.RawMessage
		if json.Unmarshal(v, &list) != nil {
			out.parts[coll] = string(v)
			continue
		}
		for i, item := range list {
			var named struct{ Name string }
			_ = json.Unmarshal(item, &named)
			key := coll + "/" + named.Name
			if named.Name == "" {
				key = coll + "/#" + string(rune('0'+i%10))
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, item, "", " "); err != nil {
				t.Fatal(err)
			}
			out.parts[key] = buf.String()
		}
	}
	return out
}

// diff lists objects only on one side, and for objects on both sides the first
// differing lines.
func (a *schemaJSON) diff(b *schemaJSON) []string {
	var out []string
	keys := map[string]bool{}
	for k := range a.parts {
		keys[k] = true
	}
	for k := range b.parts {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		x, okx := a.parts[k]
		y, oky := b.parts[k]
		switch {
		case !okx:
			out = append(out, k+": only in the init-built database")
		case !oky:
			out = append(out, k+": only in the goose-built database")
		case x != y:
			xl, yl := strings.Split(x, "\n"), strings.Split(y, "\n")
			for i := 0; i < len(xl) && i < len(yl); i++ {
				if xl[i] != yl[i] {
					out = append(out, k+": goose "+strings.TrimSpace(xl[i])+" | init "+strings.TrimSpace(yl[i]))
					break
				}
			}
			if len(xl) != len(yl) {
				out = append(out, k+": different line counts")
			}
		}
	}
	return out
}
