package migrate

import (
	"bytes"
	"database/sql"
	"io/fs"
	"strconv"
	"testing"

	momomigrate "github.com/azrtydxb/momo/migrate"
	"github.com/azrtydxb/momo/schema"
	_ "github.com/jackc/pgx/v5/stdlib" // the pgx database/sql driver

	"github.com/azrtydxb/hello/migrations"
	"github.com/azrtydxb/hello/migrations/legacy"
)

// TestMigrationsValid fails on what `momo migrate validate` reports for
// migrations/postgres: a momo.sum that does not match the files, a fork or a
// break in the snapshot chain, a hand-written migration that changes the
// schema.
func TestMigrationsValid(t *testing.T) {
	rep, err := momomigrate.ValidateOffline(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rep.Problems {
		t.Error(p)
	}
}

// TestTakeoverVersion fails unless legacy_seed is the second migration,
// right after the generated init, hand-written, with init's schema: a
// takeover records exactly these two, which are what the frozen goose set
// built.
func TestTakeoverVersion(t *testing.T) {
	v, err := TakeoverVersion()
	if err != nil {
		t.Fatal(err)
	}
	files, err := momomigrate.Discover(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	var ups []momomigrate.File
	snaps := map[string][]byte{}
	for _, f := range files {
		switch f.Kind {
		case "up":
			ups = append(ups, f)
		case "snapshot":
			snaps[f.Version] = f.Body
		}
	}
	if len(ups) < 2 || ups[0].Name != "init" || ups[1].Version != v {
		t.Fatalf("migrations start %v; want init, then %s at %s", ups[:min(2, len(ups))], TakeoverName, v)
	}
	if !bytes.Contains(ups[0].Body, []byte("-- momo:generated")) {
		t.Error("init is not generated")
	}
	if bytes.Contains(ups[1].Body, []byte("-- momo:generated")) {
		t.Errorf("%s is marked generated; it is the hand-written seed", TakeoverName)
	}
	load := func(ver string) *schema.Schema {
		t.Helper()
		s, err := momomigrate.ReadSnapshot(bytes.NewReader(snaps[ver]))
		if err != nil {
			t.Fatalf("snapshot %s: %v", ver, err)
		}
		return s.Schema
	}
	if !schema.Equal(load(ups[0].Version), load(v)) {
		t.Errorf("%s's snapshot differs from init's: the takeover would record a schema goose never built", TakeoverName)
	}
}

// TestEngine fails if momo.yaml's migrate section does not load into the
// engine hello-control runs.
func TestEngine(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://hello@127.0.0.1:1/hello") // never connects
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := Engine(db); err != nil {
		t.Fatal(err)
	}
}

func TestIsLegacyVersion(t *testing.T) {
	for v, want := range map[int64]bool{
		0: false, 1: true, 45: true, legacy.Latest: true, legacy.Latest + 1: false,
		migrations.Latest(): false, 20261117194928: false,
	} {
		if got := IsLegacyVersion(v); got != want {
			t.Errorf("IsLegacyVersion(%d) = %v, want %v", v, got, want)
		}
	}
}

// TestLatest fails if migrations.Latest, the version backups carry and
// restores compare, is not the newest migration's.
func TestLatest(t *testing.T) {
	files, err := momomigrate.Discover(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	newest := files[len(files)-1].Version
	if got := migrations.Latest(); got <= legacy.Latest || newest != strconv.FormatInt(got, 10) {
		t.Errorf("migrations.Latest() = %d, newest migration %s", got, newest)
	}
	if _, err := fs.Stat(migrations.FS, momomigrate.SumFileName); err != nil {
		t.Errorf("migrations.FS has no %s: %v", momomigrate.SumFileName, err)
	}
}
