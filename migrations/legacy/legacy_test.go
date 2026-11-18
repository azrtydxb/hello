package legacy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // the pgx database/sql driver
	"github.com/pressly/goose/v3"
)

// frozenDigest is the SHA-256 of the "<sha256>  <file>\n" lines of every file,
// in name order. It changes only if a frozen migration is edited, added or
// removed, which must not happen: momo's legacy_seed is recorded on top of
// exactly these files.
const frozenDigest = "3854d3fc1d0b49c20598f49618e11710f0b0939e7e540ee4f9b73dcd9d8ada3c"

// TestFrozen fails if the goose set changed: a schema change is a momo
// migration in migrations/postgres (docs/database.md).
func TestFrozen(t *testing.T) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	var lines strings.Builder
	var newest int64
	for _, n := range names {
		b, err := fs.ReadFile(FS, n)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(&lines, "%s  %s\n", hex.EncodeToString(sum[:]), n)
		v, err := strconv.ParseInt(strings.SplitN(n, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		newest = max(newest, v)
	}
	if newest != Latest {
		t.Errorf("newest frozen migration is %05d, Latest says %05d", newest, Latest)
	}
	sum := sha256.Sum256([]byte(lines.String()))
	if got := hex.EncodeToString(sum[:]); got != frozenDigest {
		t.Errorf("the frozen goose set changed (digest %s, want %s): add a momo migration instead (docs/database.md)", got, frozenDigest)
	}
}

// TestBootstrapDatabase is not a test: CI's momo-bootstrap job runs it to
// build the database that `momo generate --from-db` reads, with goose and
// the frozen set, at HELLO_MOMO_BOOTSTRAP_URL. It skips without it.
func TestBootstrapDatabase(t *testing.T) {
	dsn := os.Getenv("HELLO_MOMO_BOOTSTRAP_URL")
	if dsn == "" {
		t.Skip("HELLO_MOMO_BOOTSTRAP_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Up(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("goose applied %d migrations", len(res))
}
