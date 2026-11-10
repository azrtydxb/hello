package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Tools runs the PostgreSQL client programs.
type Tools interface {
	// Dump writes a custom-format dump of the database to path.
	Dump(ctx context.Context, path string) error
	// Restore loads the dump at path in one transaction, replacing the
	// existing objects.
	Restore(ctx context.Context, path string) error
}

// PGTools runs pg_dump and pg_restore, found on PATH, against one database.
// The password goes in the child's environment, never its arguments.
type PGTools struct {
	Conn *pgx.ConnConfig
}

func (t PGTools) env() []string {
	c := t.Conn
	env := append(os.Environ(),
		"PGHOST="+c.Host, "PGPORT="+strconv.Itoa(int(c.Port)), "PGUSER="+c.User,
		"PGPASSWORD="+c.Password, "PGDATABASE="+c.Database, "PGCONNECT_TIMEOUT=15")
	switch {
	case c.TLSConfig == nil:
		env = append(env, "PGSSLMODE=disable")
	case c.TLSConfig.InsecureSkipVerify:
		env = append(env, "PGSSLMODE=require")
	default:
		env = append(env, "PGSSLMODE=verify-full")
	}
	return env
}

// run executes cmd with the database's environment. Each caller names its
// program as a literal, so the command is never built from a variable.
func (t PGTools) run(cmd *exec.Cmd) error {
	if t.Conn == nil {
		return errors.New("backup: no database configuration")
	}
	cmd.Env = t.env()
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Run(); err != nil {
		// The tools' diagnostics carry no credentials; keep the tail only.
		return fmt.Errorf("%s: %w: %s", cmd.Args[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

func (t PGTools) Dump(ctx context.Context, path string) error {
	return t.run(exec.CommandContext(ctx, "pg_dump", "-Fc", "--no-owner", "--no-privileges", "--file="+path))
}

func (t PGTools) Restore(ctx context.Context, path string) error {
	return t.run(exec.CommandContext(ctx, "pg_restore", "--single-transaction", "--clean", "--if-exists",
		"--no-owner", "--no-privileges", "--exit-on-error", "--dbname="+t.Conn.Database, path))
}

// limitedWriter keeps at most n bytes and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
