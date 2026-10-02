// Package integration holds tests that need real PostgreSQL or Docker.
// They skip unless HELLO_TEST_DATABASE_URL or HELLO_DOCKER=1 is set.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3/lock"
)

const root = "../.."

func TestMigrateIdempotentConcurrent(t *testing.T) {
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
	defer func() { _ = db.Close() }()
	// Deterministic: while another session holds the migration lock,
	// Up must wait for it rather than run.
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS schema_info, goose_db_version"); err != nil {
		t.Fatal(err)
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	upDone := make(chan error, 1)
	go func() { _, err := migrate.Up(ctx, db); upDone <- err }()
	select {
	case err := <-upDone:
		t.Fatalf("migrate up ran while the migration lock was held elsewhere (err=%v)", err)
	case <-time.After(1500 * time.Millisecond):
	}
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	_ = holder.Close()
	if err := <-upDone; err != nil {
		t.Fatalf("migrate up after lock release: %v", err)
	}

	// Behavioural: 16 replicas racing over fresh schemas apply it once.
	for round := range 3 {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS schema_info, goose_db_version"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		counts := make([]int, 16)
		errs := make([]error, 16)
		for i := range counts {
			wg.Go(func() {
				conn, err := sql.Open("pgx", dsn) // separate pools, like separate replicas
				if err != nil {
					errs[i] = err
					return
				}
				defer func() { _ = conn.Close() }()
				counts[i], errs[i] = migrate.Up(ctx, conn)
			})
		}
		wg.Wait()
		total := 0
		for i := range counts {
			if errs[i] != nil {
				t.Fatalf("round %d runner %d: %v", round, i, errs[i])
			}
			total += counts[i]
		}
		if total != 1 {
			t.Fatalf("round %d: migrations applied %d times across concurrent runners, want 1", round, total)
		}
	}
	if n, err := migrate.Up(ctx, db); err != nil || n != 0 {
		t.Fatalf("second migrate up = %d, %v; want 0, nil", n, err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_info").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("schema_info rows = %d, %v; want 1", rows, err)
	}
}

// scratchDatabase creates a throwaway database next to the one dsn names, so
// the test never touches existing data, and returns its DSN.
func scratchDatabase(t *testing.T, ctx context.Context, dsn string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_it_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatalf("HELLO_TEST_DATABASE_URL must be a postgres:// URL")
	}
	u.Path = "/" + name
	return u.String()
}

func docker(t *testing.T) {
	t.Helper()
	if os.Getenv("HELLO_DOCKER") != "1" {
		t.Skip("HELLO_DOCKER=1 not set")
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestImagesNonRoot(t *testing.T) {
	docker(t)
	images := map[string][]string{
		"hello-control:test": {"build", "--target", "hello-control", "-t", "hello-control:test", "."},
		"hello-sip:test":     {"build", "--target", "hello-sip", "-t", "hello-sip:test", "."},
		"hello-ui:test":      {"build", "-t", "hello-ui:test", "web"},
	}
	for img, args := range images {
		run(t, "docker", args...)
		user := run(t, "docker", "inspect", "--format", "{{.Config.User}}", img)
		if user == "" || user == "root" || user == "0" || strings.HasPrefix(user, "0:") || strings.HasPrefix(user, "root:") {
			t.Errorf("%s runs as %q, want a non-root user", img, user)
		}
	}
}

func TestLabSmoke(t *testing.T) {
	docker(t)
	// Own project name, so cleanup never removes a developer's running lab.
	compose := []string{"compose", "-p", "hello-smoke", "-f", filepath.Join("deploy", "docker-compose", "compose.yaml")}
	t.Cleanup(func() {
		cmd := exec.Command("docker", append(compose, "down", "-v")...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("compose down: %v\n%s", err, out)
		}
	})
	run(t, "docker", append(compose, "up", "-d", "--build", "--wait", "--wait-timeout", "180")...)

	client := &http.Client{Timeout: 5 * time.Second}
	for _, url := range []string{
		"http://localhost:8080/api/v1/version", // through the UI proxy
		"http://localhost:8081/readyz",
		"http://localhost:8082/readyz",
		"http://localhost:8083/readyz",
	} {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
		}
	}
}
