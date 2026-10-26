package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store; stores with the same id share a "server".
type memStore struct {
	id   string
	mu   sync.Mutex
	data map[string][]byte // bucket/key
	// copies and puts count calls, for dedupe checks.
	copies, puts int
	// corruptPut flips a byte of every archive written.
	corruptPut bool
}

func newMem(id string) *memStore { return &memStore{id: id, data: map[string][]byte{}} }

func (m *memStore) ID() string { return m.id }

func (m *memStore) set(bucket, key string, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[bucket+"/"+key] = b
}

func (m *memStore) get(bucket, key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[bucket+"/"+key]
	return b, ok
}

func (m *memStore) List(_ context.Context, bucket, prefix string) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Object
	for k, v := range m.data {
		if rest, ok := strings.CutPrefix(k, bucket+"/"); ok && strings.HasPrefix(rest, prefix) {
			out = append(out, Object{Key: rest, Size: int64(len(v)), ETag: etag(v)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func etag(b []byte) string { return HashBytes(b).SHA256[:16] }

func (m *memStore) Stat(_ context.Context, bucket, key string) (Object, bool, error) {
	b, ok := m.get(bucket, key)
	if !ok {
		return Object{}, false, nil
	}
	return Object{Key: key, Size: int64(len(b)), ETag: etag(b)}, true, nil
}

func (m *memStore) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	b, ok := m.get(bucket, key)
	if !ok {
		return nil, errors.New("no such key")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStore) Put(_ context.Context, bucket, key string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if m.corruptPut && strings.HasPrefix(key, ArchivePrefix) {
		b[len(b)-40] ^= 0xff
	}
	m.mu.Lock()
	m.puts++
	m.mu.Unlock()
	m.set(bucket, key, b)
	return nil
}

func (m *memStore) Copy(_ context.Context, dstBucket, dstKey, srcBucket, srcKey string) error {
	b, ok := m.get(srcBucket, srcKey)
	if !ok {
		return errors.New("no such key")
	}
	m.mu.Lock()
	m.copies++
	m.mu.Unlock()
	m.set(dstBucket, dstKey, append([]byte(nil), b...))
	return nil
}

func (m *memStore) Remove(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, bucket+"/"+key)
	return nil
}

func (m *memStore) EnsureBucket(context.Context, string) error { return nil }

// fakeDB is the shared "database" two replicas talk to.
type fakeDB struct {
	mu       sync.Mutex
	set      SecretSettings
	runs     []Run
	next     int64
	locks    map[string]bool
	schema   int64
	rev      int64
	audits   []string
	bumped   int64
	finished int
}

func newFakeDB() *fakeDB {
	s := DefaultSettings()
	return &fakeDB{set: SecretSettings{Settings: s}, locks: map[string]bool{}, schema: 5, rev: 7}
}

func (f *fakeDB) BackupSettings(context.Context) (SecretSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set, nil
}

func (f *fakeDB) InsertRun(_ context.Context, r Run) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	r.ID = f.next
	f.runs = append(f.runs, r)
	return r.ID, nil
}

func (f *fakeDB) FinishRun(_ context.Context, r Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished++
	for i := range f.runs {
		if f.runs[i].ID == r.ID {
			f.runs[i] = r
		}
	}
	return nil
}

func (f *fakeDB) FailRunning(_ context.Context, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].Status == StatusRunning {
			f.runs[i].Status, f.runs[i].ErrorClass, f.runs[i].FinishedAt = StatusFailed, "interrupted", &at
		}
	}
	return nil
}

func (f *fakeDB) Runs(context.Context, int) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Run(nil), f.runs...), nil
}

func (f *fakeDB) GetRun(_ context.Context, id int64) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return Run{}, ErrNotFound
}

func (f *fakeDB) DeleteRun(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.runs {
		if r.ID == id {
			f.runs = append(f.runs[:i], f.runs[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeDB) DeleteRunByKey(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.runs[:0]
	for _, r := range f.runs {
		if r.ObjectKey != key {
			out = append(out, r)
		}
	}
	f.runs = out
	return nil
}

func (f *fakeDB) ScheduledSince(_ context.Context, t time.Time) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Run
	for i := len(f.runs) - 1; i >= 0; i-- {
		if r := f.runs[i]; r.Kind == RunScheduled && !r.StartedAt.Before(t) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeDB) LastSuccess(context.Context) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best time.Time
	for _, r := range f.runs {
		if r.Status == StatusSucceeded && r.FinishedAt != nil && r.FinishedAt.After(best) {
			best = *r.FinishedAt
		}
	}
	return best, !best.IsZero(), nil
}

func (f *fakeDB) AdoptArchive(_ context.Context, key string, size int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].ObjectKey == key {
			if f.runs[i].Status == StatusRunning {
				f.runs[i].Status, f.runs[i].ArchiveBytes = StatusSucceeded, size
			}
			return nil
		}
	}
	f.next++
	f.runs = append(f.runs, Run{ID: f.next, Kind: RunScheduled, Status: StatusSucceeded, Attempt: 1, StartedAt: at, ObjectKey: key, ArchiveBytes: size})
	return nil
}

func (f *fakeDB) SchemaVersion(context.Context) (int64, error) { return f.schema, nil }

func (f *fakeDB) ConfigRevision(context.Context) (int64, error) { return f.rev, nil }

func (f *fakeDB) BumpRevision(_ context.Context, atLeast int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bumped = atLeast
	return nil
}

func (f *fakeDB) Audit(_ context.Context, actor, action, resource, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, actor+"|"+action+"|"+resource+"|"+id)
	return nil
}

func (f *fakeDB) TryLock(_ context.Context, key string) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locks[key] {
		return nil, false, nil
	}
	f.locks[key] = true
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.locks, key)
	}, true, nil
}

// fakeTools writes a fixed "dump" and records restores.
type fakeTools struct {
	mu       sync.Mutex
	dump     []byte
	dumpErr  error
	dumps    int
	restored [][]byte
	restErr  error
	// onRestore sees whether maintenance was on.
	onRestore func()
}

func (t *fakeTools) Dump(_ context.Context, path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dumps++
	if t.dumpErr != nil {
		return t.dumpErr
	}
	return os.WriteFile(path, t.dump, 0o600)
}

func (t *fakeTools) Restore(_ context.Context, path string) error {
	if t.onRestore != nil {
		t.onRestore()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.restored = append(t.restored, b)
	return t.restErr
}

func testSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func testKey(t *testing.T) Key {
	t.Helper()
	k, err := DeriveKey(testSecret(t))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rig is one replica over shared db, live and destination stores.
type rig struct {
	db    *fakeDB
	live  *memStore
	dest  *memStore
	tools *fakeTools
	clk   *clock
	key   Key
	mails []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{db: newFakeDB(), live: newMem("live"), dest: newMem("live"), tools: &fakeTools{dump: []byte("PGDMP-fixture-database")},
		clk: &clock{t: time.Date(2026, 10, 26, 2, 0, 30, 0, time.UTC)}, key: testKey(t)}
	// The default destination is the live MinIO itself: one server.
	r.dest = r.live
	r.live.set("hello-voicemail", "box/1.wav", []byte("voicemail audio"))
	r.live.set("hello-announcements", "welcome.wav", []byte("welcome announcement"))
	return r
}

func (r *rig) service(t *testing.T) *Service {
	t.Helper()
	return New(Config{
		DB: r.db, Key: r.key, Live: r.live, Tools: r.tools, Now: r.clk.Now, Tmp: t.TempDir(), HelloVersion: "test",
		BinarySchema: 5, Retry: 15 * time.Minute,
		Dest: func(context.Context, SecretSettings) (Store, string, error) { return r.dest, DefaultBucket, nil },
		Notify: func(_ context.Context, to, subject, _ string) error {
			r.mails = append(r.mails, to+"|"+subject)
			return nil
		},
	})
}
