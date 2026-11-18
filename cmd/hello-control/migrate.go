package main

import (
	"fmt"
	"io"

	"github.com/azrtydxb/hello/internal/migrate"
)

// printStatus writes `hello-control migrate status`: one line per momo
// migration, "<version> <state> <name>" (Engine.Status), after a note when
// the database is a goose one the next `migrate up` takes over.
func printStatus(w io.Writer, rep *migrate.Report) {
	if rep.Goose > 0 && !rep.TakenOver {
		_, _ = fmt.Fprintf(w, "goose schema at %05d: the next migrate up takes it over (the frozen goose migrations, then %s is recorded as applied)\n",
			rep.Goose, migrate.TakeoverName)
	}
	for _, v := range rep.Versions {
		_, _ = fmt.Fprintf(w, "%s %-8s %s\n", v.Version, v.State, v.Name)
	}
}
