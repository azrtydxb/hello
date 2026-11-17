package integration

// The momo models (internal/db/models) against the schema goose builds.
//
// Goose stays the migration tool; momo is the query layer. A goose migration
// that changes a modeled table therefore leaves the models behind unless they
// are regenerated in the same change. This test is the gate: it migrates a
// scratch database with goose, inspects the modeled tables (introspect.only
// in momo.yaml, passed to the inspector as Config.OnlyTables) and fails when
//
//   - a model file differs from what `momo generate --from-db` writes for that
//     database (a column, a check or an index changed; a hand edit), or
//   - the schema the generated code declares (each model's
//     MomoMeta().TableSchema, written by `momo generate`) differs from the
//     inspected one.
//
// It skips without HELLO_TEST_DATABASE_URL (the integration job sets it).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/azrtydxb/momo/driver/pgxdriver"
	momoconfig "github.com/azrtydxb/momo/gen/config"
	"github.com/azrtydxb/momo/gen/fromdb"
	momomigrate "github.com/azrtydxb/momo/migrate"
	momopg "github.com/azrtydxb/momo/migrate/postgres"
	"github.com/azrtydxb/momo/schema"

	"github.com/azrtydxb/hello/internal/db/models"
	"github.com/azrtydxb/hello/internal/migrate"
)

// regenerateModels is how to bring the models up to date; the failure
// messages and docs/database.md give it.
const regenerateModels = `regenerate the models against a database goose migrated to the latest version:
    HELLO_MODELS_DATABASE_URL=postgres://... go run github.com/azrtydxb/momo/cmd/momo@v0.16.0 generate --from-db HELLO_MODELS_DATABASE_URL --no-snapshot --overwrite
    go run github.com/azrtydxb/momo/cmd/momo@v0.16.0 generate
(docs/database.md, "Regenerating the models")`

// droppedConstraints are the constraints of modeled tables that the models
// cannot declare, so `momo generate --from-db` leaves them out with a warning:
// foreign keys to tables that have no model yet. They stay in the goose
// schema; only the models do not describe them. A new warning fails the test
// so that it is looked at.
var droppedConstraints = []string{
	"crm_connections foreign key crm_connections_user_id_fkey",
	"crm_user_map foreign key crm_user_map_user_id_fkey",
}

type momoModelsRig struct {
	cfg  *momoconfig.File
	live *schema.Schema
	res  *fromdb.Result
	dir  string
}

func newMomoModelsRig(t *testing.T) *momoModelsRig {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	dsn = scratchDatabase(t, ctx, dsn)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	cfg, err := momoconfig.Load(filepath.Join(root, "momo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Introspect.Only) == 0 {
		t.Fatal("momo.yaml: introspect.only is empty: every table would be modeled")
	}
	e, err := momomigrate.NewEngine(pgxdriver.FromSQLDB(db), fstest.MapFS{},
		momomigrate.WithDialect(momopg.New()),
		momomigrate.WithConfig(momomigrate.Config{OnlyTables: cfg.Introspect.Only, Exclude: cfg.Introspect.Skip}))
	if err != nil {
		t.Fatal(err)
	}
	live, err := e.Inspect(ctx)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	var names []string
	for _, tb := range live.Tables {
		names = append(names, tb.Name)
	}
	for _, g := range cfg.Introspect.Only {
		if !slices.ContainsFunc(live.Tables, func(tb schema.Table) bool { return schema.MatchObject([]string{g}, tb.Schema, tb.Name) }) {
			t.Fatalf("momo.yaml: introspect.only %q matches no table of the goose schema (inspected: %s)", g, strings.Join(names, ", "))
		}
	}
	res, err := fromdb.Generate(live, fromdb.Options{Package: cfg.Introspect.Package, Config: cfg.Introspect})
	if err != nil {
		t.Fatalf("fromdb.Generate: %v", err)
	}
	return &momoModelsRig{cfg: cfg, live: live, res: res, dir: filepath.Join(root, cfg.Introspect.Out)}
}

// TestMomoModelsMatchSchema fails if a model file in internal/db/models is not
// what `momo generate --from-db` writes for the goose schema, a modeled table
// has no file, a file models a table that is gone, or the generator reports
// an error or a warning other than the known droppedConstraints.
func TestMomoModelsMatchSchema(t *testing.T) {
	r := newMomoModelsRig(t)
	var warns []string
	for _, f := range r.res.Findings {
		switch f.Severity {
		case fromdb.SevError:
			t.Errorf("momo generate --from-db: %s", f)
		case fromdb.SevWarn:
			warns = append(warns, f.Object)
			if !slices.Contains(droppedConstraints, f.Object) {
				t.Errorf("momo generate --from-db: new warning %s\n(a constraint the models cannot declare: if expected, add %q to droppedConstraints)", f, f.Object)
			}
		default:
			t.Logf("momo generate --from-db: %s", f)
		}
	}
	for _, want := range droppedConstraints {
		if !slices.Contains(warns, want) {
			t.Errorf("droppedConstraints lists %q, which momo generate --from-db no longer reports: remove it", want)
		}
	}
	if r.res.Failed() {
		t.Fatalf("momo generate --from-db cannot model the goose schema; nothing to compare")
	}

	want := map[string]bool{}
	stale := false
	for _, f := range r.res.Files {
		want[f.Name] = true
		path := filepath.Join(r.dir, f.Name)
		got, err := os.ReadFile(path) // #nosec G304 -- a file of the repository's model directory
		if err != nil {
			stale = true
			t.Errorf("%s: missing (table %s): %v", path, f.Table, err)
			t.Logf("expected %s:\n%s", f.Name, f.Content)
			continue
		}
		gotBody, _, ok := fromdb.SplitHeader(got)
		wantBody, _, _ := fromdb.SplitHeader(f.Content)
		if !ok || !bytes.Equal(gotBody, wantBody) {
			stale = true
			t.Errorf("%s: differs from the table %s in the goose schema", path, f.Table)
			t.Logf("expected %s:\n%s", f.Name, f.Content)
		}
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || want[n] || !strings.HasSuffix(n, ".go") || n == "doc.go" || strings.HasSuffix(n, "_gen.go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		stale = true
		t.Errorf("%s: models no table of introspect.only (a table that was dropped or removed from momo.yaml): delete it", filepath.Join(r.dir, n))
	}
	if stale {
		t.Errorf("the models in %s are stale: %s", r.cfg.Introspect.Out, regenerateModels)
	}
}

// declaredTables is the schema each generated model declares (momo_gen.go),
// one entry per table of introspect.only.
func declaredTables() []*schema.Table {
	return []*schema.Table{
		(*models.CrmConnector)(nil).MomoMeta().TableSchema,
		(*models.CrmConnection)(nil).MomoMeta().TableSchema,
		(*models.CrmOauthState)(nil).MomoMeta().TableSchema,
		(*models.CrmCallLog)(nil).MomoMeta().TableSchema,
		(*models.CrmUserMap)(nil).MomoMeta().TableSchema,
	}
}

// TestMomoModelsDeclareSchema fails if the schema the generated code declares
// (each model's MomoMeta().TableSchema, written by `momo generate`) differs
// from the modeled tables of the goose schema, less the constraints the
// models cannot declare (droppedConstraints): a column, type, default,
// check, index or key added, changed or removed by a goose migration, or
// model files regenerated without running `momo generate` afterwards.
func TestMomoModelsDeclareSchema(t *testing.T) {
	r := newMomoModelsRig(t)
	if r.res.Failed() {
		t.Fatal("momo generate --from-db cannot model the goose schema (TestMomoModelsMatchSchema lists why)")
	}
	declared := &schema.Schema{Dialect: r.res.Snapshot.Dialect}
	for _, tb := range declaredTables() {
		declared.Tables = append(declared.Tables, *tb)
	}
	if len(declared.Tables) != len(r.res.Snapshot.Tables) {
		t.Errorf("the models declare %d tables, introspect.only matches %d: add the new model to declaredTables, and %s",
			len(declared.Tables), len(r.res.Snapshot.Tables), regenerateModels)
	}
	goose := valueListChecks(r.res.Snapshot)
	declared = valueListChecks(declared)
	if schema.Equal(goose, declared) {
		return
	}
	changes, _, err := momomigrate.Diff(declared, goose, momomigrate.DiffOptions{AllowDestructive: true})
	if err != nil {
		t.Fatalf("diff the declared and the goose schema: %v", err)
	}
	for _, c := range changes {
		before, _ := json.Marshal(c.Before)
		after, _ := json.Marshal(c.After)
		t.Errorf("%s %s.%s: declared %s, goose schema %s", c.Kind, c.Table, c.Name, before, after)
	}
	if len(changes) == 0 {
		a, _ := json.MarshalIndent(schema.Normalized(declared), "", "  ")
		b, _ := json.MarshalIndent(schema.Normalized(goose), "", "  ")
		t.Errorf("the declared schema differs from the goose schema in a way Diff does not report:\ndeclared:\n%s\ngoose:\n%s", a, b)
	}
	t.Errorf("the generated code in internal/db/models does not match the goose schema: %s", regenerateModels)
}

// valueListChecks returns a copy of s in which every CHECK with a value list
// (schema.Check.In, `c.CheckIn` in a model) has the expression momo renders
// for that list (Check.ExprFor). A value-list check is defined by its column
// and values: migrate.Diff compares it as a set, but schema.Equal (momo
// v0.16.0) still compares the expression text, which differs between the
// declaration (`"status" IN (...)`) and PostgreSQL's spelling of the goose
// constraint (`status = ANY (ARRAY[...])`). s is not modified.
func valueListChecks(s *schema.Schema) *schema.Schema {
	c := *s
	c.Tables = slices.Clone(s.Tables)
	for i := range c.Tables {
		t := &c.Tables[i]
		t.Checks = slices.Clone(t.Checks)
		for j := range t.Checks {
			t.Checks[j].Expr = t.Checks[j].ExprFor(s.Dialect)
		}
	}
	return &c
}
