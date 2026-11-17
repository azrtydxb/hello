// Package migrations embeds Hello's PostgreSQL schema migrations, momo
// migrations (github.com/azrtydxb/momo/migrate) generated from the models in
// internal/db/models, so hello-control carries its own schema.
//
// postgres/ holds the up files, their snapshots and momo.sum (format 2;
// .gitattributes merges it with union). The goose migrations that built the
// schema before the cutover are frozen in the legacy subpackage.
// docs/database.md describes how to add a migration.
package migrations

import (
	"embed"
	"io/fs"
	"strconv"
	"strings"
)

//go:embed postgres
var files embed.FS

// FS is the migration directory: the files of postgres/ at its root.
var FS = mustSub(files, "postgres")

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // the directory is embedded above
	}
	return s
}

// Latest is the newest migration version in FS (a 14-digit UTC timestamp,
// YYYYMMDDHHMMSS, as an integer), counting the snapshot of a Go migration
// too; 0 when FS has none.
func Latest() int64 {
	ents, err := fs.ReadDir(FS, ".")
	if err != nil {
		return 0
	}
	var newest int64
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".up.sql") && !strings.HasSuffix(n, ".snapshot.json") {
			continue
		}
		v, _, ok := strings.Cut(n, "_")
		if !ok || len(v) != 14 {
			continue
		}
		if x, err := strconv.ParseInt(v, 10, 64); err == nil {
			newest = max(newest, x)
		}
	}
	return newest
}
