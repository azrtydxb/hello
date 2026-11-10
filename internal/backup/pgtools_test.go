package backup

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeTool writes an executable named name into dir. It records its argv to
// argsFile and exits with code.
func fakeTool(t *testing.T, dir, name, argsFile string, code int) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPGToolsRunFromPATH checks that pg_dump is found on PATH and gets the
// dump arguments, and that a failing tool's error names it.
func TestPGToolsRunFromPATH(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://hello:secret@db.local:5432/hello")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	fakeTool(t, dir, "pg_dump", argsFile, 0)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tools := PGTools{Conn: cfg}
	if err := tools.Dump(context.Background(), "/tmp/out.dump"); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-Fc", "--no-owner", "--no-privileges", "--file=/tmp/out.dump"} {
		if !strings.Contains(string(got), want+"\n") {
			t.Errorf("pg_dump argv lacks %q; got %q", want, got)
		}
	}

	fakeTool(t, dir, "pg_dump", argsFile, 3)
	err = tools.Dump(context.Background(), "/tmp/out.dump")
	if err == nil || !strings.HasPrefix(err.Error(), "pg_dump: ") {
		t.Fatalf("failing pg_dump error = %v, want a message starting with pg_dump:", err)
	}
}
