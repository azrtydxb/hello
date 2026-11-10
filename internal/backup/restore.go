package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// RestoreOptions selects what Restore does.
type RestoreOptions struct {
	// DryRun verifies the archive and reports counts; nothing is written.
	DryRun bool
	Actor  string
	// ID is the backup's run id for the audit row; 0 when restoring by key
	// from the command line.
	ID int64
}

// RestoreReport holds counts only.
type RestoreReport struct {
	DryRun        bool      `json:"dryRun"`
	HelloVersion  string    `json:"helloVersion"`
	SchemaVersion int64     `json:"schemaVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	DBBytes       int64     `json:"dbBytes"`
	Objects       int       `json:"objects"`
	ObjectBytes   int64     `json:"objectBytes"`
	// ObjectsToRestore is how many live objects are missing or differ.
	ObjectsToRestore int `json:"objectsToRestore"`
	// ObjectsMissingInBackup is how many objects the archive lists that its
	// store no longer holds; a real restore refuses while it is not zero.
	ObjectsMissingInBackup int `json:"objectsMissingInBackup"`
}

// ConfirmText is the text a restore of backup id must be confirmed with.
func ConfirmText(id int64) string { return fmt.Sprintf("RESTORE %d", id) }

// RestoreRun restores backup id from the configured destination.
func (s *Service) RestoreRun(ctx context.Context, id int64, confirm string, opts RestoreOptions) (RestoreReport, error) {
	r, err := s.DB.GetRun(ctx, id)
	if err != nil {
		return RestoreReport{}, err
	}
	if !opts.DryRun && confirm != ConfirmText(id) {
		return RestoreReport{}, ErrConfirm
	}
	if r.Status != StatusSucceeded || r.ObjectKey == "" {
		return RestoreReport{}, fmt.Errorf("%w: that backup did not complete", ErrNotFound)
	}
	set, err := s.DB.BackupSettings(ctx)
	if err != nil {
		return RestoreReport{}, err
	}
	dest, bucket, err := s.Dest(ctx, set)
	if err != nil {
		return RestoreReport{}, err
	}
	opts.ID = id
	return s.Restore(ctx, dest, bucket, r.ObjectKey, opts)
}

// Restore verifies the archive at key in dest/bucket and, unless DryRun,
// replaces the database with its dump and restores the objects. The steps
// follow spec S-4: verify the manifest and every part, refuse a newer
// schema or another key, enter maintenance, pg_restore in one transaction,
// restore missing or changed objects (never deleting any), rebuild the
// snapshot and leave maintenance.
func (s *Service) Restore(ctx context.Context, dest Store, bucket, key string, opts RestoreOptions) (RestoreReport, error) {
	unlock, ok, err := s.DB.TryLock(ctx, LockKey)
	if err != nil {
		return RestoreReport{}, err
	}
	if !ok {
		return RestoreReport{}, ErrBusy
	}
	defer unlock()

	dir, err := s.tmpDir()
	if err != nil {
		return RestoreReport{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	dumpPath := path.Join(dir, "db.dump")
	if opts.DryRun {
		dumpPath = ""
	}

	rc, err := dest.Get(ctx, bucket, key)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	a, err := s.readArchive(rc, dumpPath)
	_ = rc.Close()
	if err != nil {
		return RestoreReport{}, err
	}
	m := a.Manifest
	rep := RestoreReport{DryRun: opts.DryRun, HelloVersion: m.HelloVersion, SchemaVersion: m.SchemaVersion, CreatedAt: m.CreatedAt,
		DBBytes: m.Counts.DBBytes, Objects: len(a.Objects)}
	if m.SchemaVersion > s.BinarySchema {
		return rep, ErrNewerSchema
	}
	var todo []ObjectEntry
	for _, e := range a.Objects {
		rep.ObjectBytes += e.Size
		if _, have, err := dest.Stat(ctx, bucket, ObjectsPrefix+e.SHA256); err != nil {
			return rep, fail("storage", err)
		} else if !have {
			rep.ObjectsMissingInBackup++
			continue
		}
		differs, err := s.liveDiffers(ctx, e)
		if err != nil {
			return rep, fail("source", err)
		}
		if differs {
			todo = append(todo, e)
		}
	}
	rep.ObjectsToRestore = len(todo)
	if opts.DryRun {
		return rep, nil
	}
	if rep.ObjectsMissingInBackup > 0 {
		return rep, fmt.Errorf("%w: %d objects are missing from the backup store", ErrVerify, rep.ObjectsMissingInBackup)
	}

	// The pre-restore revision keeps the snapshot revision rising.
	preRev, _ := s.DB.ConfigRevision(ctx)
	// Leaving maintenance must happen even when ctx ended.
	bg := context.WithoutCancel(ctx)
	s.Maintenance.Enter(bg)
	defer s.Maintenance.Leave(bg)

	outcome := "ok"
	defer func() {
		id := fmt.Sprintf("%d outcome=%s objects=%d restored=%d", opts.ID, outcome, rep.Objects, rep.ObjectsToRestore)
		if opts.ID == 0 {
			id = fmt.Sprintf("cli outcome=%s objects=%d restored=%d", outcome, rep.Objects, rep.ObjectsToRestore)
		}
		actor := opts.Actor
		if actor == "" {
			actor = "system"
		}
		if err := s.DB.Audit(bg, actor, "restore", "backup", id); err != nil {
			s.Log.Error("backup restore: audit", "error", err)
		}
	}()

	if err := s.Tools.Restore(ctx, dumpPath); err != nil {
		outcome = "failed"
		return rep, fail("db_restore", err)
	}
	for _, e := range todo {
		if err := s.Live.EnsureBucket(ctx, e.Bucket); err != nil {
			outcome = "failed-objects"
			return rep, fail("source", err)
		}
		if err := CopyObject(ctx, dest, bucket, ObjectsPrefix+e.SHA256, s.Live, e.Bucket, e.Key, e.Size); err != nil {
			outcome = "failed-objects"
			return rep, fail("storage", err)
		}
	}
	if m.SchemaVersion < s.BinarySchema && s.Migrate != nil {
		if err := s.Migrate(ctx); err != nil {
			outcome = "failed-migrate"
			return rep, fail("migrate", err)
		}
	}
	if err := s.reconcile(ctx, dest, bucket); err != nil {
		s.Log.Warn("backup restore: reconcile runs", "error", err)
	}
	if err := s.DB.BumpRevision(ctx, preRev); err != nil {
		outcome = "failed-snapshot"
		return rep, fail("snapshot", err)
	}
	return rep, nil
}

// liveDiffers reports whether the live object is absent or has other bytes.
func (s *Service) liveDiffers(ctx context.Context, e ObjectEntry) (bool, error) {
	o, ok, err := s.Live.Stat(ctx, e.Bucket, e.Key)
	if err != nil {
		return false, err
	}
	if !ok || o.Size != e.Size {
		return true, nil
	}
	if o.ETag != "" && o.ETag == e.ETag {
		return false, nil
	}
	sum, err := s.hashLive(ctx, e.Bucket, e.Key)
	if err != nil {
		return false, err
	}
	return sum != e.SHA256, nil
}

// reconcile makes the restored table list the archives the destination
// holds: a dump catches its own run still running, and archives made after
// it are unknown to the dump.
func (s *Service) reconcile(ctx context.Context, dest Store, bucket string) error {
	arch, err := dest.List(ctx, bucket, ArchivePrefix)
	if err != nil {
		return err
	}
	for _, a := range arch {
		at := parseArchiveTime(a.Key)
		if at.IsZero() {
			continue
		}
		if err := s.DB.AdoptArchive(ctx, a.Key, a.Size, at); err != nil {
			return err
		}
	}
	// Whatever is still running was caught mid-run by the dump.
	if err := s.DB.FailRunning(ctx, s.Now()); err != nil && !errors.Is(err, context.Canceled) {
		s.Log.Warn("backup restore: stale runs", "error", err)
	}
	return nil
}

// parseArchiveTime reads the UTC time from archives/hello-backup-<utc>.hbk.
func parseArchiveTime(key string) time.Time {
	n := strings.TrimSuffix(strings.TrimPrefix(path.Base(key), "hello-backup-"), ".hbk")
	t, err := time.Parse("20060102T150405Z", n)
	if err != nil {
		return time.Time{}
	}
	return t
}
