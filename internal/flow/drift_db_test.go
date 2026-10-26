package flow

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/azrtydxb/hello/internal/migrate"
)

// TestCatalogueDrift fails if the catalogue and the places that store or
// accept destinations disagree (spec flow-canvas S-1, S-3):
//   - a destination kind in a database CHECK constraint (read with
//     pg_get_constraintdef) or an OpenAPI enum has no catalogue node;
//   - a catalogue connection maps to a kind the database refuses, or the
//     database stores a kind the catalogue does not offer;
//   - a CHECK constraint lists destination kinds but is not in KindFields;
//   - a feature-code action is not classified.
//
// The database part runs where HELLO_TEST_DATABASE_URL is set (CI's
// integration job); the rest runs everywhere.
func TestCatalogueDrift(t *testing.T) {
	t.Run("stored kinds", testStoredKinds)
	t.Run("go lists", testGoKindLists)
	t.Run("openapi", testOpenAPIKindEnums)
	t.Run("database", testDatabaseKinds)
}

var quotedRe = regexp.MustCompile(`'([^']*)'`)

// scratchMigrated creates a throwaway, migrated database and opens it.
func scratchMigrated(t *testing.T) *sql.DB {
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
	name := fmt.Sprintf("hello_flow_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
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

func testDatabaseKinds(t *testing.T) {
	db := scratchMigrated(t)
	rows, err := db.QueryContext(context.Background(), `SELECT c.conrelid::regclass::text, c.conname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c WHERE c.contype = 'c' AND c.connamespace = 'public'::regnamespace ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type check struct {
		table, name string
		vals        []string
	}
	byName := map[string]check{}
	var all []check
	for rows.Next() {
		var table, name, def string
		if err := rows.Scan(&table, &name, &def); err != nil {
			t.Fatal(err)
		}
		var vals []string
		for _, m := range quotedRe.FindAllStringSubmatch(def, -1) {
			vals = append(vals, m[1])
		}
		c := check{table, name, vals}
		byName[table+"."+name] = c
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	cat := Get()
	listed := map[string]bool{}
	for _, f := range KindFields {
		key := f.Table + "." + f.Constraint
		listed[key] = true
		c, ok := byName[key]
		if !ok {
			t.Errorf("KindFields lists %s, which is not a CHECK constraint in the migrated schema", key)
			continue
		}
		if p := checkKindList(cat, key, c.vals); len(p) > 0 {
			t.Error(strings.Join(p, "\n"))
		}
		// Every kind the column accepts must be one the catalogue port offers.
		port, _ := cat.Port(f.Node, f.Port)
		offered := map[string]bool{}
		for _, tg := range port.Targets {
			offered[tg.Kind+"."+tg.Port] = true
		}
		set, _ := reaches(c.vals)
		for r := range set {
			if !offered[r] {
				t.Errorf("%s accepts a kind that leads to %s, but %s.%s does not offer it", key, r, f.Node, f.Port)
			}
		}
	}
	for _, c := range all {
		key := c.table + "." + c.name
		if listed[key] {
			continue
		}
		switch {
		case featureActionValues(c.vals):
			if p := checkFeatureActions(key, c.vals); len(p) > 0 {
				t.Error(strings.Join(p, "\n"))
			}
		case destinationValues(c.vals):
			if _, ok := NotDestinationConstraints[c.name]; ok {
				continue
			}
			t.Errorf("%s lists destination kinds %v but is not in KindFields (kinds.go): add it with the output port that draws its edge, or list it in NotDestinationConstraints with the reason", key, c.vals)
		}
	}
	for name := range NotDestinationConstraints {
		found := false
		for _, c := range all {
			found = found || c.name == name
		}
		if !found {
			t.Errorf("NotDestinationConstraints lists %s, which is not a CHECK constraint", name)
		}
	}
}
