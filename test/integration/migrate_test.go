package integration

// momo's migration engine and the takeover of a goose-built database
// (internal/migrate). Every database here is a scratch one.

import (
	"bytes"
	"context"
	"database/sql"
	"hash/fnv"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	momomigrate "github.com/azrtydxb/momo/migrate"
	"github.com/azrtydxb/momo/schema"
	"github.com/pressly/goose/v3"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/migrations"
	"github.com/azrtydxb/hello/migrations/legacy"
)

// momoHistoryLockID is the advisory lock momo's engine takes for the history
// table in schema: FNV-64a of "momo:<schema>.momo_migrations"
// (docs/migrations.md in momo, "Lock").
func momoHistoryLockID(schemaName string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("momo:" + schemaName + ".momo_migrations"))
	return int64(h.Sum64()) // #nosec G115 -- the same wrap momo does
}

// momoMigrations lists the versions of the momo migrations, in order.
func momoMigrations(t *testing.T) []string {
	t.Helper()
	files, err := momomigrate.Discover(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if f.Kind == "up" {
			out = append(out, f.Version)
		}
	}
	return out
}

// legacyAfter is how many frozen goose migrations are newer than v.
func legacyAfter(t *testing.T, v int64) int {
	t.Helper()
	names, err := fs.Glob(legacy.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, name := range names {
		x, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if x > v {
			n++
		}
	}
	return n
}

// momoAfterTakeover is how many momo migrations are newer than legacy_seed:
// the ones a takeover applies after recording the rest.
func momoAfterTakeover(t *testing.T) int {
	t.Helper()
	takeover, err := migrate.TakeoverVersion()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, v := range momoMigrations(t) {
		if v > takeover {
			n++
		}
	}
	return n
}

// openScratch opens a new scratch database.
func openScratch(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", scratchDatabase(t, context.Background(), dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// gooseUpTo builds db with the frozen goose migrations up to v, as a
// pre-cutover install at that version.
func gooseUpTo(t *testing.T, db *sql.DB, v int64) {
	t.Helper()
	p, err := goose.NewProvider(goose.DialectPostgres, db, legacy.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(context.Background(), v); err != nil {
		t.Fatalf("goose up to %05d: %v", v, err)
	}
}

// schemaBuilds are the two ways a current schema comes about: a new install
// (every momo migration) and a pre-cutover install taken over from goose.
var schemaBuilds = []struct {
	name  string
	build func(t *testing.T, db *sql.DB)
}{
	{"fresh", func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := migrate.Up(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}},
	{"taken over from goose", func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := migrate.LegacyUp(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		if _, err := migrate.Up(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}},
}

// assertTakenOver fails unless db's momo history records every migration
// through legacy_seed once, as a baseline that ran no statement, and goose's
// history is still there.
func assertTakenOver(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	takeover, err := migrate.TakeoverVersion()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, v := range momoMigrations(t) {
		if v <= takeover {
			want = append(want, v)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT version, status, statements_applied, apply_id FROM momo_migrations
		WHERE version <> '__momo_meta__' AND version <= $1 ORDER BY version`, takeover)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var v, status, applyID string
		var n int
		if err := rows.Scan(&v, &status, &n, &applyID); err != nil {
			t.Fatal(err)
		}
		if status != "applied" || n != 0 || applyID != momomigrate.BaselineApplyID {
			t.Errorf("history %s: status %s, %d statements, apply_id %s; want applied, 0, %s", v, status, n, applyID, momomigrate.BaselineApplyID)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("baselined versions %v, want %v", got, want)
	}
	var goose int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version WHERE is_applied`).Scan(&goose); err != nil || goose == 0 {
		t.Errorf("goose_db_version after the takeover: %d rows, %v", goose, err)
	}
}

// assertNoDrift fails unless the live schema of db is the snapshot of its
// newest applied migration, every body compared (strict drift).
func assertNoDrift(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	e, err := migrate.Engine(db)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.DriftWith(ctx, momomigrate.DriftOptions{Strict: true})
	if err != nil {
		t.Fatalf("drift: %v", err)
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
}

// TestMigrateFresh fails if a new install does not run every momo
// migration once (counted from the engine's report), or ends with a schema
// other than the newest snapshot's or without the rows the goose
// migrations used to insert.
func TestMigrateFresh(t *testing.T) {
	db := openScratch(t)
	ctx := context.Background()
	n, err := migrate.Up(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(momoMigrations(t)); n != want {
		t.Fatalf("migrate up on an empty database = %d, want %d (every momo migration)", n, want)
	}
	if n, err := migrate.Up(ctx, db); err != nil || n != 0 {
		t.Fatalf("second migrate up = %d, %v; want 0, nil", n, err)
	}
	assertNoDrift(t, ctx, db)
	var goose bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&goose); err != nil || goose {
		t.Fatalf("a new install has goose_db_version (%v, %v)", goose, err)
	}
	rep, err := migrate.Status(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Goose != 0 || !rep.TakenOver || len(rep.Versions) != len(momoMigrations(t)) {
		t.Fatalf("status %+v", rep)
	}
	for _, v := range rep.Versions {
		if v.State != momomigrate.StateApplied {
			t.Errorf("status: %s %s is %s", v.Version, v.Name, v.State)
		}
	}
	if v, err := store.New(db, nil).SchemaVersion(ctx); err != nil || v != migrations.Latest() {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, migrations.Latest())
	}
}

// TestMigrateTakeover fails if a pre-cutover database, at the newest goose
// migration or behind it, is not taken over: the missing goose migrations
// run, momo records init and legacy_seed without running them, the schema
// is the snapshot's, goose's history stays, and an older image (goose up)
// then finds nothing to do.
func TestMigrateTakeover(t *testing.T) {
	for _, from := range []int64{legacy.Latest, 100} {
		t.Run(strconv.FormatInt(from, 10), func(t *testing.T) {
			db := openScratch(t)
			ctx := context.Background()
			gooseUpTo(t, db, from)

			rep, err := migrate.Status(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Goose != from || rep.TakenOver {
				t.Fatalf("status before the takeover: goose %d, taken over %v", rep.Goose, rep.TakenOver)
			}
			if v, err := store.New(db, nil).SchemaVersion(ctx); err != nil || v != from {
				t.Fatalf("SchemaVersion before the takeover = %d, %v; want %d", v, err, from)
			}

			after := momoAfterTakeover(t)
			n, err := migrate.Up(ctx, db)
			if err != nil {
				t.Fatalf("takeover: %v", err)
			}
			if want := legacyAfter(t, from) + after; n != want {
				t.Fatalf("takeover ran %d migrations, want %d (goose after %05d, then momo after legacy_seed)", n, want, from)
			}
			assertTakenOver(t, ctx, db)
			assertNoDrift(t, ctx, db)
			if n, err := migrate.Up(ctx, db); err != nil || n != 0 {
				t.Fatalf("migrate up after the takeover = %d, %v; want 0, nil", n, err)
			}
			if n, err := migrate.LegacyUp(ctx, db); err != nil || n != 0 {
				t.Fatalf("goose up after the takeover (an older image) = %d, %v; want 0, nil", n, err)
			}
			if rep, err = migrate.Status(ctx, db); err != nil || !rep.TakenOver {
				t.Fatalf("status after the takeover: %+v, %v", rep, err)
			}
			if v, err := store.New(db, nil).SchemaVersion(ctx); err != nil || v != migrations.Latest() {
				t.Fatalf("SchemaVersion after the takeover = %d, %v; want %d", v, err, migrations.Latest())
			}
		})
	}
}

// TestMigrateInitParity fails if a database goose built and momo then took
// over differs from one momo's migrations built: in the schema (every
// table, column, index, constraint, sequence, function and trigger) or in
// the rows the migrations insert (legacy_seed against the goose
// migrations' own inserts) and the sequence positions.
func TestMigrateInitParity(t *testing.T) {
	ctx := context.Background()
	gooseDB, freshDB := openScratch(t), openScratch(t)
	schemaBuilds[1].build(t, gooseDB)
	schemaBuilds[0].build(t, freshDB)

	inspect := func(db *sql.DB) *schema.Schema {
		t.Helper()
		e, err := migrate.EngineFor(db, fstest.MapFS{})
		if err != nil {
			t.Fatal(err)
		}
		s, err := e.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	g, f := inspect(gooseDB), inspect(freshDB)
	t.Logf("inspected %d tables, %d sequences, %d functions, %d triggers", len(g.Tables), len(g.Sequences), len(g.Functions), len(g.Triggers))
	if len(g.Tables) == 0 || len(g.Tables) != len(f.Tables) || len(g.Sequences) != len(f.Sequences) ||
		len(g.Functions) != len(f.Functions) || len(g.Triggers) != len(f.Triggers) {
		t.Errorf("object counts differ: goose %d tables, %d sequences, %d functions, %d triggers; momo %d, %d, %d, %d",
			len(g.Tables), len(g.Sequences), len(g.Functions), len(g.Triggers),
			len(f.Tables), len(f.Sequences), len(f.Functions), len(f.Triggers))
	}
	// The catalog as PostgreSQL itself renders it, which also covers what
	// momo's schema model leaves out (a constraint's deferrability, say).
	gc, fc := catalog(t, ctx, gooseDB), catalog(t, ctx, freshDB)
	for _, k := range slices.Sorted(maps.Keys(gc)) {
		if fc[k] != gc[k] {
			t.Errorf("%s:\n  goose-built %q\n  momo-built  %q", k, gc[k], fc[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(fc)) {
		if _, ok := gc[k]; !ok {
			t.Errorf("%s: only momo-built: %q", k, fc[k])
		}
	}
	t.Logf("compared %d catalog objects", len(gc))

	gr, fr := tableRows(t, ctx, gooseDB, g), tableRows(t, ctx, freshDB, f)
	for _, tb := range g.Tables {
		if !bytes.Equal(gr[tb.Name], fr[tb.Name]) {
			t.Errorf("table %s: rows differ\ngoose-built: %s\nmomo-built:  %s", tb.Name, gr[tb.Name], fr[tb.Name])
		}
	}
	gseq, fseq := sequencePositions(t, ctx, gooseDB), sequencePositions(t, ctx, freshDB)
	for name, pos := range gseq {
		if fseq[name] != pos {
			t.Errorf("sequence %s: goose-built at %s, momo-built at %s", name, pos, fseq[name])
		}
	}
	if len(gseq) != len(fseq) {
		t.Errorf("%d sequences goose-built, %d momo-built", len(gseq), len(fseq))
	}
}

// catalog describes every object of db's current schema the way
// PostgreSQL renders it: columns (position among the live ones, type,
// nullability, default, identity, generated, collation), constraints and
// indexes (pg_get_constraintdef, pg_get_indexdef), triggers, functions,
// sequence options, table kinds and options, and comments. The two migration
// histories are left out. A value list's order is not significant (momo
// renders a c.CheckIn's values sorted), so ARRAY[...] in a check is sorted.
func catalog(t *testing.T, ctx context.Context, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	const ns = `(SELECT oid FROM pg_namespace WHERE nspname = current_schema())`
	const notHistory = ` NOT IN ('goose_db_version', 'goose_db_version_id_seq', 'momo_migrations')`
	for _, q := range []string{
		`SELECT 'table ' || c.relname, c.relkind::text || ' ' || c.relpersistence::text || ' ' || coalesce(array_to_string(c.reloptions, ','), '') || ' ' || coalesce(obj_description(c.oid, 'pg_class'), '')
		   FROM pg_class c WHERE c.relnamespace = ` + ns + ` AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND c.relname` + notHistory,
		`SELECT 'column ' || c.relname || '.' || a.attname,
		        (row_number() OVER (PARTITION BY c.oid ORDER BY a.attnum))::text || ' ' || format_type(a.atttypid, a.atttypmod) || ' ' || a.attnotnull::text || ' ' ||
		        coalesce(pg_get_expr(d.adbin, d.adrelid), '') || ' ' || a.attidentity::text || ' ' || a.attgenerated::text || ' ' || coalesce(co.collname, '') || ' ' ||
		        coalesce(col_description(c.oid, a.attnum), '')
		   FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		   LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		   LEFT JOIN pg_collation co ON co.oid = a.attcollation AND a.attcollation <> 0 AND co.collname <> 'default'
		  WHERE c.relnamespace = ` + ns + ` AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped AND c.relname` + notHistory,
		`SELECT 'constraint ' || c.conrelid::regclass::text || '.' || c.conname, pg_get_constraintdef(c.oid)
		   FROM pg_constraint c WHERE c.connamespace = ` + ns + ` AND c.conrelid::regclass::text` + notHistory,
		`SELECT 'index ' || i.indexrelid::regclass::text, pg_get_indexdef(i.indexrelid)
		   FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid WHERE c.relnamespace = ` + ns + ` AND c.relname` + notHistory,
		`SELECT 'trigger ' || tg.tgrelid::regclass::text || '.' || tg.tgname, pg_get_triggerdef(tg.oid)
		   FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid WHERE c.relnamespace = ` + ns + ` AND NOT tg.tgisinternal`,
		`SELECT 'function ' || p.oid::regprocedure::text, pg_get_functiondef(p.oid)
		   FROM pg_proc p WHERE p.pronamespace = ` + ns,
		`SELECT 'sequence ' || s.sequencename, s.data_type::text || ' ' || s.start_value::text || ' ' || s.min_value::text || ' ' || s.max_value::text || ' ' ||
		        s.increment_by::text || ' ' || s.cycle::text || ' ' || s.cache_size::text
		   FROM pg_sequences s WHERE s.schemaname = current_schema() AND s.sequencename` + notHistory,
	} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("catalog: %v\n%s", err, q)
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(k, "constraint ") && strings.HasPrefix(v, "CHECK ") {
				v = sortArrays(v)
			}
			out[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return out
}

// arrayLit is an ARRAY[...] literal of a check: its elements hold no "]".
var arrayLit = regexp.MustCompile(`ARRAY\[([^\]]*)\]`)

// sortArrays sorts the elements of every ARRAY[...] literal in def.
func sortArrays(def string) string {
	return arrayLit.ReplaceAllStringFunc(def, func(m string) string {
		el := strings.Split(m[len("ARRAY["):len(m)-1], ", ")
		slices.Sort(el)
		return "ARRAY[" + strings.Join(el, ", ") + "]"
	})
}

// tableRows returns each table's rows as sorted JSON, without the
// timestamptz columns (goose and legacy_seed both stamp now()).
func tableRows(t *testing.T, ctx context.Context, db *sql.DB, s *schema.Schema) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, tb := range s.Tables {
		var drop []string
		for _, c := range tb.Columns {
			if strings.HasPrefix(strings.ToLower(c.Type), "timestamp") {
				drop = append(drop, "'"+c.Name+"'")
			}
		}
		row := "to_jsonb(x)"
		if len(drop) > 0 {
			row = "(to_jsonb(x) - ARRAY[" + strings.Join(drop, ",") + "])"
		}
		var b []byte
		// #nosec G202 -- table and column names come from the inspected schema
		q := `SELECT coalesce(jsonb_agg(r ORDER BY r::text), '[]')::text FROM (SELECT ` + row + ` AS r FROM "` + tb.Name + `" x) y`
		if err := db.QueryRowContext(ctx, q).Scan(&b); err != nil {
			t.Fatalf("rows of %s: %v", tb.Name, err)
		}
		out[tb.Name] = b
	}
	return out
}

// sequencePositions returns "last_value is_called" of every sequence.
func sequencePositions(t *testing.T, ctx context.Context, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT sequencename, coalesce(last_value::text, 'unset') FROM pg_sequences
		WHERE schemaname = current_schema() AND sequencename NOT LIKE 'goose_db_version%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			t.Fatal(err)
		}
		out[n] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigrateRollback fails if a hand-written migration with a .down.sql
// on top of Hello's migrations does not apply, roll back through
// Engine.Rollback (its row rolled_back, the change undone) and apply again.
func TestMigrateRollback(t *testing.T) {
	db := openScratch(t)
	ctx := context.Background()
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	dir := toyMigration(t)
	e, err := migrate.EngineFor(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	rev := func() int64 {
		t.Helper()
		var v int64
		if err := db.QueryRowContext(ctx, `SELECT config_revision FROM schema_info`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	base := rev()
	rep, err := e.Apply(ctx, momomigrate.ApplyOptions{})
	if err != nil || len(rep.Applied) != 1 || rep.Applied[0].Version != toyVersion {
		t.Fatalf("apply the toy migration: %+v, %v", rep, err)
	}
	if got := rev(); got != base+1000 {
		t.Fatalf("after up: config_revision %d, want %d", got, base+1000)
	}
	if rep, err = e.Rollback(ctx, momomigrate.RollbackOptions{}); err != nil || len(rep.Applied) != 1 || rep.Applied[0].Version != toyVersion {
		t.Fatalf("rollback: %+v, %v", rep, err)
	}
	if got := rev(); got != base {
		t.Fatalf("after down: config_revision %d, want %d", got, base)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM momo_migrations WHERE version = $1`, toyVersion).Scan(&status); err != nil || status != "rolled_back" {
		t.Fatalf("toy history row: %q, %v; want rolled_back", status, err)
	}
	if rep, err = e.Apply(ctx, momomigrate.ApplyOptions{}); err != nil || len(rep.Applied) != 1 {
		t.Fatalf("apply after the rollback: %+v, %v", rep, err)
	}
	if got := rev(); got != base+1000 {
		t.Fatalf("after up again: config_revision %d, want %d", got, base+1000)
	}
}

// toyVersion is later than any real migration.
const toyVersion = "29991231235959"

// toyMigration is migrations.FS plus a hand-written migration with a down
// file, its snapshot (the newest one, re-versioned) and a momo.sum.
func toyMigration(t *testing.T) fs.FS {
	t.Helper()
	dir := fstest.MapFS{}
	err := fs.WalkDir(migrations.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations.FS, p)
		dir[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := momomigrate.LatestSnapshot(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	snap := *latest
	snap.ParentVersion, snap.Version, snap.Base = latest.Version, toyVersion, ""
	var b bytes.Buffer
	if err := momomigrate.WriteSnapshot(&b, snap); err != nil {
		t.Fatal(err)
	}
	dir[toyVersion+"_toy.snapshot.json"] = &fstest.MapFile{Data: b.Bytes()}
	dir[toyVersion+"_toy.up.sql"] = &fstest.MapFile{Data: []byte("UPDATE schema_info SET config_revision = config_revision + 1000;\n")}
	dir[toyVersion+"_toy.down.sql"] = &fstest.MapFile{Data: []byte("UPDATE schema_info SET config_revision = config_revision - 1000;\n")}
	delete(dir, momomigrate.SumFileName)
	sum, err := momomigrate.ComputeSum(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sb bytes.Buffer // not b: the snapshot's MapFile holds b's bytes
	if err := sum.Write(&sb); err != nil {
		t.Fatal(err)
	}
	dir[momomigrate.SumFileName] = &fstest.MapFile{Data: sb.Bytes()}
	return dir
}
