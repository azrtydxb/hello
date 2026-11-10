package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/azrtydxb/hello/internal/backup"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
)

// memObjects is an in-memory object server for the restore test: one server,
// so copies stay inside it as they do on the bundled MinIO.
type memObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memObjects) ID() string { return "mem" }

func (m *memObjects) List(_ context.Context, bucket, prefix string) ([]backup.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []backup.Object
	for k, v := range m.data {
		if rest, ok := strings.CutPrefix(k, bucket+"/"); ok && strings.HasPrefix(rest, prefix) {
			out = append(out, backup.Object{Key: rest, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memObjects) Stat(_ context.Context, bucket, key string) (backup.Object, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[bucket+"/"+key]
	return backup.Object{Key: key, Size: int64(len(v))}, ok, nil
}

func (m *memObjects) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[bucket+"/"+key]
	if !ok {
		return nil, errors.New("no such key")
	}
	return io.NopCloser(bytes.NewReader(v)), nil
}

func (m *memObjects) Put(_ context.Context, bucket, key string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[bucket+"/"+key] = b
	return err
}

func (m *memObjects) Copy(_ context.Context, dstBucket, dstKey, srcBucket, srcKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[srcBucket+"/"+srcKey]
	if !ok {
		return errors.New("no such key")
	}
	m.data[dstBucket+"/"+dstKey] = append([]byte(nil), v...)
	return nil
}

func (m *memObjects) Remove(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, bucket+"/"+key)
	return nil
}

func (m *memObjects) EnsureBucket(context.Context, string) error { return nil }

func (m *memObjects) get(bucket, key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[bucket+"/"+key]
	return string(v), ok
}

func (m *memObjects) set(bucket, key, v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[bucket+"/"+key] = []byte(v)
}

func tableCounts(t *testing.T, ctx context.Context, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	out := map[string]int{}
	for _, n := range names {
		q := `SELECT count(*) FROM "` + n + `"`
		if n == "audit_events" { // the restore itself adds one audit row
			q += ` WHERE action <> 'restore'`
		}
		var c int
		if err := db.QueryRowContext(ctx, q).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", n, err)
		}
		out[n] = c
	}
	return out
}

// TestRestoreIntegration backs up a migrated database with its objects and
// restores it into an empty one with the real pg_dump and pg_restore: every
// table's row count matches, an archive of a newer schema is refused and
// changes nothing, and restoring objects never deletes one.
func TestRestoreIntegration(t *testing.T) {
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	ctx := context.Background()
	key, err := backup.DeriveKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}

	open := func(dsn string) (*sql.DB, *pgx.ConnConfig) {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		db := stdlib.OpenDB(*cfg)
		t.Cleanup(func() { _ = db.Close() })
		return db, cfg
	}
	service := func(db *sql.DB, cfg *pgx.ConnConfig, objs *memObjects) *backup.Service {
		return backup.New(backup.Config{
			DB: store.New(db), Key: key, Live: objs, Tools: backup.PGTools{Conn: cfg}, Tmp: t.TempDir(),
			Dest: func(context.Context, backup.SecretSettings) (backup.Store, string, error) {
				return objs, backup.DefaultBucket, nil
			},
			Migrate: func(ctx context.Context) error { _, err := migrate.Up(ctx, db); return err },
		})
	}

	// The source: migrated, with rows in several tables and two objects.
	srcDB, srcCfg := open(scratchDatabase(t, ctx, dsn))
	if _, err := migrate.Up(ctx, srcDB); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (username, password_hash) VALUES ('alice', 'x'), ('bob', 'x')`,
		`INSERT INTO audit_events (actor, action, resource, resource_id) VALUES ('user:alice', 'create', 'user', '1')`,
		`INSERT INTO backup_settings (id, keep_count, cron) VALUES (TRUE, 3, '30 3 * * *')`,
	} {
		if _, err := srcDB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	objs := &memObjects{data: map[string][]byte{}}
	objs.set("hello-voicemail", "box/1.wav", "voicemail audio")
	objs.set("hello-announcements", "welcome.wav", "welcome announcement")
	if err := service(srcDB, srcCfg, objs).RunNow(ctx, backup.RunManual); err != nil {
		t.Fatalf("backup: %v", err)
	}
	arch, _ := objs.List(ctx, backup.DefaultBucket, backup.ArchivePrefix)
	if len(arch) != 1 {
		t.Fatalf("archives: %v", arch)
	}
	want := tableCounts(t, ctx, srcDB)

	// The live objects drift before the restore.
	objs.set("hello-voicemail", "box/1.wav", "overwritten")
	_ = objs.Remove(ctx, "hello-announcements", "welcome.wav")
	objs.set("hello-voicemail", "box/extra.wav", "made later")

	// An archive of a newer schema than the binary's is refused, and the
	// empty target stays empty.
	emptyDB, emptyCfg := open(scratchDatabase(t, ctx, dsn))
	older := service(emptyDB, emptyCfg, objs)
	older.BinarySchema = backup.LatestSchema() - 1
	if _, err := older.Restore(ctx, objs, backup.DefaultBucket, arch[0].Key, backup.RestoreOptions{Actor: "test"}); !errors.Is(err, backup.ErrNewerSchema) {
		t.Fatalf("newer schema: %v", err)
	}
	var n int
	if err := emptyDB.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused restore touched the database: %d tables, %v", n, err)
	}

	// The restore into the empty database.
	rep, err := service(emptyDB, emptyCfg, objs).Restore(ctx, objs, backup.DefaultBucket, arch[0].Key, backup.RestoreOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rep.ObjectsToRestore != 2 || rep.Objects != 2 {
		t.Fatalf("report %+v", rep)
	}
	got := tableCounts(t, ctx, emptyDB)
	for table, c := range want {
		if got[table] != c {
			t.Errorf("table %s: %d rows after restore, want %d", table, got[table], c)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d tables after restore, want %d", len(got), len(want))
	}
	var status string
	if err := emptyDB.QueryRowContext(ctx, `SELECT status FROM backup_runs`).Scan(&status); err != nil || status != backup.StatusSucceeded {
		t.Fatalf("restored run row: %q, %v", status, err)
	}

	// Objects came back, and the one made after the backup was not deleted.
	if v, _ := objs.get("hello-voicemail", "box/1.wav"); v != "voicemail audio" {
		t.Errorf("changed object = %q", v)
	}
	if v, _ := objs.get("hello-announcements", "welcome.wav"); v != "welcome announcement" {
		t.Errorf("missing object = %q", v)
	}
	if v, ok := objs.get("hello-voicemail", "box/extra.wav"); !ok || v != "made later" {
		t.Errorf("an object absent from the backup was deleted")
	}
	// The restore left an audit row with its outcome and no names.
	var audit string
	if err := emptyDB.QueryRowContext(ctx, `SELECT resource_id FROM audit_events WHERE action = 'restore'`).Scan(&audit); err != nil || !strings.Contains(audit, "outcome=ok") {
		t.Fatalf("restore audit: %q, %v", audit, err)
	}
}
