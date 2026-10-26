package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// LockKey is the advisory lock a backup or restore holds, so exactly one
// replica runs one at a time.
const LockKey = "hello:backup:run"

// LiveBuckets are the MinIO buckets a backup copies: voicemail audio and
// message media, recordings, and announcements (prompts, ringtones and
// music on hold).
var LiveBuckets = []string{"hello-voicemail", "hello-recordings", "hello-announcements"}

// ObjectEntry is one live object in objects.json.
type ObjectEntry struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	ETag   string `json:"etag"`
}

// DestFunc builds the destination store and bucket from the settings.
type DestFunc func(ctx context.Context, s SecretSettings) (Store, string, error)

// Config wires a Service.
type Config struct {
	DB   DB
	Key  Key
	Live Store
	// Buckets are the live buckets to back up; nil means LiveBuckets.
	Buckets []string
	Dest    DestFunc
	Tools   Tools
	// Notify sends the failure email; nil when SMTP is not configured.
	Notify func(ctx context.Context, to, subject, body string) error
	// Migrate brings an older restored schema up to this binary's.
	Migrate      func(ctx context.Context) error
	Maintenance  *Maintenance
	Now          func() time.Time
	Tmp          string
	HelloVersion string
	// BinarySchema is the highest migration this binary has; 0 means
	// LatestSchema().
	BinarySchema int64
	// Retry is the wait before a failed scheduled run is tried again (15 m).
	Retry time.Duration
	// Tick is how often the scheduler looks at the clock (30 s).
	Tick   time.Duration
	Log    *slog.Logger
	Metric prometheus.Gauge
}

// Service runs backups and restores.
type Service struct {
	Config
	wg   sync.WaitGroup
	mu   sync.Mutex
	base context.Context
}

// New builds a Service.
func New(c Config) *Service {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Buckets == nil {
		c.Buckets = LiveBuckets
	}
	if c.Retry == 0 {
		c.Retry = 15 * time.Minute
	}
	if c.Tick == 0 {
		c.Tick = 30 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.BinarySchema == 0 {
		c.BinarySchema = LatestSchema()
	}
	if c.Maintenance == nil {
		c.Maintenance = &Maintenance{}
	}
	return &Service{Config: c, base: context.Background()}
}

// Wait blocks until background runs finish (tests).
func (s *Service) Wait() { s.wg.Wait() }

// classed is an error with a short class for the run row; the class never
// carries names, paths or secrets.
type classed struct {
	class string
	err   error
}

func (c *classed) Error() string { return c.class + ": " + c.err.Error() }
func (c *classed) Unwrap() error { return c.err }

func fail(class string, err error) error { return &classed{class: class, err: err} }

func classOf(err error) string {
	var c *classed
	switch {
	case errors.As(err, &c):
		return c.class
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	}
	return "internal"
}

// Serve is the scheduler: every replica runs it, and the run itself is taken
// under the advisory lock, so one replica does the work. It returns when ctx
// ends.
func (s *Service) Serve(ctx context.Context) {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		if err := s.Tick1(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("backup scheduler", "error", err)
		}
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

func (s *Service) baseCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

func (s *Service) updateMetric(ctx context.Context) {
	if s.Metric == nil {
		return
	}
	if at, ok, err := s.DB.LastSuccess(ctx); err == nil && ok {
		s.Metric.Set(float64(at.Unix()))
	}
}

// due returns the attempt number to run now, 0 for none, or -1 when the
// latest run still says running: its holder may be dead, which only the lock
// can tell.
func (s *Service) due(ctx context.Context, fire time.Time) (int, error) {
	runs, err := s.DB.ScheduledSince(ctx, fire)
	if err != nil {
		return 0, err
	}
	if len(runs) == 0 {
		return 1, nil
	}
	last := runs[0]
	switch {
	case last.Status == StatusRunning:
		return -1, nil
	case last.Status != StatusFailed || last.Attempt >= 2:
		return 0, nil
	case last.FinishedAt == nil || s.Now().Sub(*last.FinishedAt) >= s.Retry:
		return 2, nil
	}
	return 0, nil
}

// Tick1 is one pass of the scheduler: it refreshes the metric and starts the
// due run, if any, under the lock. Exported for tests.
func (s *Service) Tick1(ctx context.Context) error {
	s.updateMetric(ctx)
	set, err := s.DB.BackupSettings(ctx)
	if err != nil {
		return err
	}
	if !set.Enabled {
		return nil
	}
	sched, err := ParseSchedule(set.Cron)
	if err != nil {
		return err
	}
	fire := sched.Prev(s.Now())
	if fire.IsZero() {
		return nil
	}
	if n, err := s.due(ctx, fire); err != nil || n == 0 {
		return err
	}
	unlock, ok, err := s.DB.TryLock(ctx, LockKey)
	if err != nil || !ok {
		return err
	}
	defer unlock()
	// Holding the lock, a run still marked running belongs to a dead holder.
	if err := s.DB.FailRunning(ctx, s.Now()); err != nil {
		return err
	}
	// Another replica may have run between the check and the lock.
	n, err := s.due(ctx, fire)
	if err != nil || n <= 0 {
		return err
	}
	// A failed run is recorded, logged and retried; it is not a scheduler error.
	_, err = s.runLocked(ctx, RunScheduled, n)
	return err
}

// StartManual begins a manual run in the background and returns its row; the
// caller sees ErrBusy while another backup or restore holds the lock.
func (s *Service) StartManual(ctx context.Context) (Run, error) {
	unlock, ok, err := s.DB.TryLock(ctx, LockKey)
	if err != nil {
		return Run{}, err
	}
	if !ok {
		return Run{}, ErrBusy
	}
	set, err := s.DB.BackupSettings(ctx)
	if err != nil {
		unlock()
		return Run{}, err
	}
	r, err := s.begin(ctx, RunManual, 1)
	if err != nil {
		unlock()
		return Run{}, err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer unlock()
		// A manual run outlives its request, not the service.
		_ = s.finish(s.baseCtx(), r, set) // recorded in the run row
	}()
	return r, nil
}

// RunNow runs a backup in the foreground under the lock (tests, tools).
func (s *Service) RunNow(ctx context.Context, kind string) error {
	unlock, ok, err := s.DB.TryLock(ctx, LockKey)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	defer unlock()
	runErr, err := s.runLocked(ctx, kind, 1)
	if err != nil {
		return err
	}
	return runErr
}

func (s *Service) begin(ctx context.Context, kind string, attempt int) (Run, error) {
	// Under the lock, a running row belongs to a dead holder.
	if err := s.DB.FailRunning(ctx, s.Now()); err != nil {
		return Run{}, err
	}
	r := Run{Kind: kind, Status: StatusRunning, Attempt: attempt, StartedAt: s.Now().UTC()}
	r.ObjectKey = ArchivePrefix + "hello-backup-" + r.StartedAt.Format("20060102T150405Z") + ".hbk"
	id, err := s.DB.InsertRun(ctx, r)
	if err != nil {
		return Run{}, err
	}
	r.ID = id
	return r, nil
}

// runLocked runs one backup with the lock held. It returns the run's own
// error apart from infrastructure errors (settings, the run row).
func (s *Service) runLocked(ctx context.Context, kind string, attempt int) (runErr, err error) {
	set, err := s.DB.BackupSettings(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.begin(ctx, kind, attempt)
	if err != nil {
		return nil, err
	}
	return s.finish(ctx, r, set), nil
}

// finish executes the run and records the outcome. It returns the run's error.
func (s *Service) finish(ctx context.Context, r Run, set SecretSettings) error {
	err := s.execute(ctx, &r, set)
	end := s.Now().UTC()
	r.FinishedAt = &end
	if err != nil {
		r.Status, r.ErrorClass = StatusFailed, classOf(err)
		s.Log.Warn("backup failed", "run", r.ID, "attempt", r.Attempt, "class", r.ErrorClass, "error", err)
	} else {
		r.Status = StatusSucceeded
		s.Log.Info("backup finished", "run", r.ID, "objects", r.ObjectCount, "archive_bytes", r.ArchiveBytes)
	}
	// The outcome must be recorded even when the run's context ended.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if ferr := s.DB.FinishRun(fctx, r); ferr != nil {
		s.Log.Error("backup: record outcome", "run", r.ID, "error", ferr)
	}
	s.updateMetric(fctx)
	if err != nil && r.Kind == RunScheduled && r.Attempt >= 2 {
		s.alert(fctx, set, r)
	}
	return err
}

func (s *Service) alert(ctx context.Context, set SecretSettings, r Run) {
	if s.Notify == nil || set.NotifyEmail == "" {
		return
	}
	body := fmt.Sprintf("The scheduled backup failed twice (run %d, error class %s).\r\nThe last successful backup is the one before it; see Settings, Backups.\r\n", r.ID, r.ErrorClass)
	if err := s.Notify(ctx, set.NotifyEmail, "Hello PBX backup failed", body); err != nil {
		s.Log.Warn("backup: failure email not sent", "error", err)
	}
}

func (s *Service) tmpDir() (string, error) {
	d, err := os.MkdirTemp(s.Tmp, "hello-backup-")
	if err != nil {
		return "", fail("tmp", err)
	}
	return d, nil
}

// execute builds, uploads, verifies and prunes one archive.
func (s *Service) execute(ctx context.Context, r *Run, set SecretSettings) error {
	dest, bucket, err := s.Dest(ctx, set)
	if err != nil {
		return fail("settings", err)
	}
	if err := dest.EnsureBucket(ctx, bucket); err != nil {
		return fail("storage", err)
	}
	dir, err := s.tmpDir()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	dumpPath := path.Join(dir, "db.dump")
	if err := s.Tools.Dump(ctx, dumpPath); err != nil {
		return fail("db_dump", err)
	}
	prev := s.previousObjects(ctx, dest, bucket)
	entries, err := s.copyObjects(ctx, dest, bucket, prev)
	if err != nil {
		return err
	}
	objJSON, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	for _, e := range entries {
		r.ObjectBytes += e.Size
	}
	r.ObjectCount = len(entries)

	schema, err := s.DB.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(dumpPath) //nolint:gosec // G304: a file in this run's private temp directory
	if err != nil {
		return fail("tmp", err)
	}
	defer func() { _ = f.Close() }()
	info, err := HashReader(f)
	if err != nil {
		return fail("tmp", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail("tmp", err)
	}
	r.DBBytes = info.Size
	m := Manifest{HelloVersion: s.HelloVersion, SchemaVersion: schema, CreatedAt: r.StartedAt, RunID: r.ID,
		Counts: Counts{Objects: r.ObjectCount, ObjectBytes: r.ObjectBytes}}
	archPath := path.Join(dir, "archive.hbk")
	af, err := os.Create(archPath) //nolint:gosec // G304: a file in this run's private temp directory
	if err != nil {
		return fail("tmp", err)
	}
	defer func() { _ = af.Close() }()
	if err := Build(af, s.Key, m, objJSON, info, f); err != nil {
		return fail("archive", err)
	}
	st, err := af.Stat()
	if err != nil {
		return fail("tmp", err)
	}
	if _, err := af.Seek(0, io.SeekStart); err != nil {
		return fail("tmp", err)
	}
	r.ArchiveBytes = st.Size()
	if err := dest.Put(ctx, bucket, r.ObjectKey, af, st.Size()); err != nil {
		return fail("storage", err)
	}
	if err := s.verify(ctx, dest, bucket, r.ObjectKey); err != nil {
		return err
	}
	s.prune(ctx, dest, bucket, set.KeepCount)
	return nil
}

// previousObjects loads the newest archive's object list, to reuse hashes of
// unchanged objects; any failure just means everything is hashed again.
func (s *Service) previousObjects(ctx context.Context, dest Store, bucket string) map[string]ObjectEntry {
	arch, err := dest.List(ctx, bucket, ArchivePrefix)
	if err != nil || len(arch) == 0 {
		return nil
	}
	sort.Slice(arch, func(i, j int) bool { return arch[i].Key > arch[j].Key })
	for _, a := range arch {
		if es, err := s.archiveObjects(ctx, dest, bucket, a.Key); err == nil {
			out := make(map[string]ObjectEntry, len(es))
			for _, e := range es {
				out[e.Bucket+"\x00"+e.Key] = e
			}
			return out
		}
	}
	return nil
}

// archiveObjects reads manifest and objects.json of an archive, stopping
// before the dump.
func (s *Service) archiveObjects(ctx context.Context, dest Store, bucket, key string) ([]ObjectEntry, error) {
	rc, err := dest.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	ar, err := Open(rc, s.Key)
	if err != nil {
		return nil, err
	}
	for {
		name, body, err := ar.Next()
		if err != nil {
			return nil, ErrCorrupt
		}
		if name != PartObjects {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(body, 1<<30))
		if err != nil {
			return nil, err
		}
		var es []ObjectEntry
		if err := json.Unmarshal(b, &es); err != nil {
			return nil, ErrCorrupt
		}
		return es, nil
	}
}

func (s *Service) hashLive(ctx context.Context, bucket, key string) (string, error) {
	rc, err := s.Live.Get(ctx, bucket, key)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyObjects lists the live buckets, hashes each object and copies those the
// destination lacks to objects/<sha256>. Objects already stored under their
// hash are not copied again, and copies within one server never pass through
// this process.
func (s *Service) copyObjects(ctx context.Context, dest Store, bucket string, prev map[string]ObjectEntry) ([]ObjectEntry, error) {
	var out []ObjectEntry
	for _, b := range s.Buckets {
		objs, err := s.Live.List(ctx, b, "")
		if err != nil {
			return nil, fail("source", err)
		}
		for _, o := range objs {
			e := ObjectEntry{Bucket: b, Key: o.Key, Size: o.Size, ETag: o.ETag}
			if p, ok := prev[b+"\x00"+o.Key]; ok && o.ETag != "" && p.ETag == o.ETag && p.Size == o.Size {
				e.SHA256 = p.SHA256
			} else if e.SHA256, err = s.hashLive(ctx, b, o.Key); err != nil {
				return nil, fail("source", err)
			}
			dst := ObjectsPrefix + e.SHA256
			if have, ok, err := dest.Stat(ctx, bucket, dst); err != nil {
				return nil, fail("storage", err)
			} else if !ok || have.Size != e.Size {
				if err := CopyObject(ctx, s.Live, b, o.Key, dest, bucket, dst, o.Size); err != nil {
					return nil, fail("storage", err)
				}
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// opened is a verified archive.
type opened struct {
	Manifest Manifest
	Objects  []ObjectEntry
}

// readArchive streams an archive from r, checks every part against the
// manifest, and writes db.dump to dbPath when it is not empty. It returns
// ErrKeyMismatch for another key's archive and ErrVerify for a part whose
// hash differs from the manifest.
func (s *Service) readArchive(r io.Reader, dbPath string) (*opened, error) {
	ar, err := Open(r, s.Key)
	if err != nil {
		return nil, err
	}
	out := &opened{}
	seen := map[string]bool{}
	for {
		name, body, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch name {
		case PartManifest:
			b, err := io.ReadAll(io.LimitReader(body, 1<<20))
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(b, &out.Manifest); err != nil {
				return nil, ErrCorrupt
			}
			if out.Manifest.KeyID != s.Key.Fingerprint() {
				return nil, ErrKeyMismatch
			}
		case PartObjects, PartDB:
			if !seen[PartManifest] {
				return nil, ErrCorrupt
			}
			var (
				info PartInfo
				buf  bytes.Buffer
			)
			switch {
			case name == PartDB && dbPath != "":
				f, err := os.Create(dbPath) //nolint:gosec // G304: a file in this run's private temp directory
				if err != nil {
					return nil, err
				}
				info, err = HashReader(io.TeeReader(body, f))
				if cerr := f.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					return nil, err
				}
			case name == PartDB:
				if info, err = HashReader(body); err != nil {
					return nil, err
				}
			default:
				if info, err = HashReader(io.TeeReader(io.LimitReader(body, 1<<30), &buf)); err != nil {
					return nil, err
				}
			}
			want, ok := out.Manifest.Parts[name]
			if !ok || want != info {
				return nil, fmt.Errorf("%w: part %s differs from the manifest", ErrVerify, name)
			}
			if name == PartObjects {
				if err := json.Unmarshal(buf.Bytes(), &out.Objects); err != nil {
					return nil, ErrCorrupt
				}
			}
		default:
			if _, err := io.Copy(io.Discard, body); err != nil {
				return nil, err
			}
		}
		seen[name] = true
	}
	for _, n := range []string{PartManifest, PartObjects, PartDB} {
		if !seen[n] {
			return nil, fmt.Errorf("%w: part %s is missing", ErrCorrupt, n)
		}
	}
	return out, nil
}

// verify re-reads the stored archive: the manifest, and the SHA-256 of
// db.dump recomputed from the stored bytes.
func (s *Service) verify(ctx context.Context, dest Store, bucket, key string) error {
	rc, err := dest.Get(ctx, bucket, key)
	if err != nil {
		return fail("storage", err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := s.readArchive(rc, ""); err != nil {
		return fail("verify", err)
	}
	return nil
}

// prune keeps the newest keep archives and removes the older ones with their
// rows, then deletes stored objects no remaining archive references. A
// failure is logged; the run itself has already succeeded.
func (s *Service) prune(ctx context.Context, dest Store, bucket string, keep int) {
	arch, err := dest.List(ctx, bucket, ArchivePrefix)
	if err != nil {
		s.Log.Warn("backup prune: list", "error", err)
		return
	}
	sort.Slice(arch, func(i, j int) bool { return arch[i].Key > arch[j].Key })
	if keep < 1 {
		keep = DefaultKeep
	}
	for i := keep; i < len(arch); i++ {
		if err := dest.Remove(ctx, bucket, arch[i].Key); err != nil {
			s.Log.Warn("backup prune: remove", "error", err)
			return
		}
		if err := s.DB.DeleteRunByKey(ctx, arch[i].Key); err != nil {
			s.Log.Warn("backup prune: row", "error", err)
		}
	}
	if len(arch) > keep {
		arch = arch[:keep]
	}
	s.collect(ctx, dest, bucket, arch)
}

// collect deletes objects/<sha> that none of the archives references. If any
// archive cannot be read, nothing is deleted.
func (s *Service) collect(ctx context.Context, dest Store, bucket string, arch []Object) {
	used := map[string]bool{}
	for _, a := range arch {
		es, err := s.archiveObjects(ctx, dest, bucket, a.Key)
		if err != nil {
			s.Log.Warn("backup gc: archive unreadable; skipping collection", "error", err)
			return
		}
		for _, e := range es {
			used[ObjectsPrefix+e.SHA256] = true
		}
	}
	all, err := dest.List(ctx, bucket, ObjectsPrefix)
	if err != nil {
		s.Log.Warn("backup gc: list", "error", err)
		return
	}
	for _, o := range all {
		if !used[o.Key] && strings.HasPrefix(o.Key, ObjectsPrefix) {
			if err := dest.Remove(ctx, bucket, o.Key); err != nil {
				s.Log.Warn("backup gc: remove", "error", err)
				return
			}
		}
	}
}

// Delete removes an archive and its row.
func (s *Service) Delete(ctx context.Context, actor string, id int64) error {
	r, err := s.DB.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == StatusRunning {
		return ErrBusy
	}
	set, err := s.DB.BackupSettings(ctx)
	if err != nil {
		return err
	}
	if r.ObjectKey != "" {
		dest, bucket, err := s.Dest(ctx, set)
		if err != nil {
			return err
		}
		if err := dest.Remove(ctx, bucket, r.ObjectKey); err != nil {
			return fail("storage", err)
		}
	}
	if err := s.DB.DeleteRun(ctx, id); err != nil {
		return err
	}
	return s.DB.Audit(ctx, actor, "delete", "backup", fmt.Sprint(id))
}
