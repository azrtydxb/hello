package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackupArchive(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	if err := s.RunNow(ctx, RunManual); err != nil {
		t.Fatal(err)
	}
	runs, _ := r.db.Runs(ctx, 10)
	if len(runs) != 1 || runs[0].Status != StatusSucceeded || runs[0].ObjectCount != 2 {
		t.Fatalf("runs: %+v", runs)
	}
	if !strings.HasPrefix(runs[0].ObjectKey, "archives/hello-backup-20261026T020030Z") || !strings.HasSuffix(runs[0].ObjectKey, ".hbk") {
		t.Fatalf("object key %q", runs[0].ObjectKey)
	}
	raw, ok := r.dest.get(DefaultBucket, runs[0].ObjectKey)
	if !ok {
		t.Fatal("archive not stored")
	}
	if bytes.Contains(raw, []byte("PGDMP-fixture-database")) {
		t.Fatal("archive is not encrypted")
	}

	// It decrypts with the key and the manifest holds every part hash.
	m, err := ReadManifest(bytes.NewReader(raw), r.key)
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != FormatVersion || m.SchemaVersion != 5 || m.HelloVersion != "test" || m.Counts.Objects != 2 || m.KeyID != r.key.Fingerprint() {
		t.Fatalf("manifest %+v", m)
	}
	if got := m.Parts[PartDB]; got != HashBytes([]byte("PGDMP-fixture-database")) {
		t.Fatalf("db part %+v", got)
	}
	if _, ok := m.Parts[PartObjects]; !ok {
		t.Fatal("objects.json hash missing")
	}
	op, err := s.readArchive(bytes.NewReader(raw), "")
	if err != nil || len(op.Objects) != 2 {
		t.Fatalf("read: %v %+v", err, op)
	}
	// The objects were copied by the server under their hash.
	for _, e := range op.Objects {
		if _, ok := r.dest.get(DefaultBucket, ObjectsPrefix+e.SHA256); !ok {
			t.Fatalf("object %s not stored under its hash", e.Key)
		}
	}
	if r.live.copies != 2 || r.live.puts != 1 { // two server-side copies, one archive upload
		t.Fatalf("copies=%d puts=%d", r.live.copies, r.live.puts)
	}

	// Another key's fingerprint is refused, before any decryption.
	if _, err := ReadManifest(bytes.NewReader(raw), testKey(t)); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("other key: %v", err)
	}

	// An object with the same SHA-256 is not copied again.
	r.clk.Add(time.Hour)
	if err := s.RunNow(ctx, RunManual); err != nil {
		t.Fatal(err)
	}
	if r.live.copies != 2 {
		t.Fatalf("unchanged objects copied again: %d", r.live.copies)
	}
}

func TestArchiveTamper(t *testing.T) {
	k := testKey(t)
	var buf bytes.Buffer
	big := bytes.Repeat([]byte("x"), 3*chunkSize+17)
	if err := Write(&buf, k, []Part{{"a", bytes.NewReader(big)}, {"b", strings.NewReader("tail")}}); err != nil {
		t.Fatal(err)
	}
	read := func(b []byte) error {
		ar, err := Open(bytes.NewReader(b), k)
		if err != nil {
			return err
		}
		for {
			_, body, err := ar.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := io.Copy(io.Discard, body); err != nil {
				return err
			}
		}
	}
	if err := read(buf.Bytes()); err != nil {
		t.Fatalf("intact: %v", err)
	}
	flipped := bytes.Clone(buf.Bytes())
	flipped[len(flipped)/2] ^= 1
	if err := read(flipped); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("flipped byte: %v", err)
	}
	// A part cut short (end frame removed) is detected too.
	if err := read(buf.Bytes()[:len(buf.Bytes())-60]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestBackupRunLeader(t *testing.T) {
	r := newRig(t)
	a, b := r.service(t), r.service(t)
	ctx := context.Background()

	// Two replicas tick at once: one backup.
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Tick1(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if r.tools.dumps != 1 {
		t.Fatalf("dumps = %d, want 1", r.tools.dumps)
	}
	// The same fire time does not run again.
	for _, s := range []*Service{a, b} {
		if err := s.Tick1(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r.tools.dumps != 1 {
		t.Fatalf("second tick ran again: %d", r.tools.dumps)
	}
	// While one holds the lock, a manual run is refused.
	unlock, _, _ := r.db.TryLock(ctx, LockKey)
	if _, err := a.StartManual(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("manual while locked: %v", err)
	}
	unlock()
}

func TestBackupRetryOnce(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	r.db.set.NotifyEmail = "ops@example.com"
	r.tools.dumpErr = errors.New("boom")

	if err := s.Tick1(ctx); err != nil {
		t.Fatal(err)
	}
	if r.tools.dumps != 1 {
		t.Fatalf("first attempt: %d", r.tools.dumps)
	}
	// Not before 15 minutes.
	r.clk.Add(14 * time.Minute)
	_ = s.Tick1(ctx)
	if r.tools.dumps != 1 {
		t.Fatalf("retried early: %d", r.tools.dumps)
	}
	if len(r.mails) != 0 {
		t.Fatal("alert sent after the first failure")
	}
	r.clk.Add(2 * time.Minute)
	_ = s.Tick1(ctx)
	if r.tools.dumps != 2 {
		t.Fatalf("retry: %d", r.tools.dumps)
	}
	// The second failure alerts, and there is no third attempt.
	if len(r.mails) != 1 || !strings.HasPrefix(r.mails[0], "ops@example.com|") {
		t.Fatalf("alert: %v", r.mails)
	}
	r.clk.Add(time.Hour)
	_ = s.Tick1(ctx)
	if r.tools.dumps != 2 {
		t.Fatalf("third attempt: %d", r.tools.dumps)
	}
	runs, _ := r.db.Runs(ctx, 10)
	if len(runs) != 2 || runs[0].ErrorClass != "db_dump" || runs[1].Attempt != 2 {
		t.Fatalf("runs: %+v", runs)
	}
	// Next day's fire time starts over, and a success needs no retry.
	r.tools.dumpErr = nil
	r.clk.Add(24 * time.Hour)
	_ = s.Tick1(ctx)
	if r.tools.dumps != 3 {
		t.Fatalf("next day: %d", r.tools.dumps)
	}
}

func TestBackupVerifyFailsOnChangedPart(t *testing.T) {
	r := newRig(t)
	r.dest.corruptPut = true
	s := r.service(t)
	ctx := context.Background()
	err := s.RunNow(ctx, RunManual)
	if err == nil || classOf(err) != "verify" {
		t.Fatalf("err = %v", err)
	}
	runs, _ := r.db.Runs(ctx, 10)
	if len(runs) != 1 || runs[0].Status != StatusFailed || runs[0].ErrorClass != "verify" {
		t.Fatalf("runs: %+v", runs)
	}
	// The archive stays for inspection.
	if _, ok := r.dest.get(DefaultBucket, runs[0].ObjectKey); !ok {
		t.Fatal("archive was removed")
	}
}

func TestBackupRetention(t *testing.T) {
	r := newRig(t)
	r.db.set.KeepCount = 2
	s := r.service(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if i == 2 {
			// A changed object leaves the first archive's object unreferenced.
			r.live.set("hello-voicemail", "box/1.wav", []byte("new audio"))
		}
		if err := s.RunNow(ctx, RunManual); err != nil {
			t.Fatal(err)
		}
		r.clk.Add(time.Minute)
	}
	arch, _ := r.dest.List(ctx, DefaultBucket, ArchivePrefix)
	if len(arch) != 2 {
		t.Fatalf("archives kept: %d", len(arch))
	}
	runs, _ := r.db.Runs(ctx, 10)
	if len(runs) != 2 {
		t.Fatalf("rows kept: %d", len(runs))
	}
	// The old voicemail audio is only referenced by the second archive now,
	// and still stored; nothing referenced is lost.
	objs, _ := r.dest.List(ctx, DefaultBucket, ObjectsPrefix)
	if len(objs) != 3 {
		t.Fatalf("stored objects: %d", len(objs))
	}
	op, err := s.archiveObjects(ctx, r.dest, DefaultBucket, arch[0].Key)
	if err != nil || len(op) != 2 {
		t.Fatalf("archive objects: %v %v", err, op)
	}
}

func TestRestore(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	if err := s.RunNow(ctx, RunManual); err != nil {
		t.Fatal(err)
	}
	runs, _ := r.db.Runs(ctx, 10)
	id := runs[0].ID

	// Live state drifts: one object changed, one removed, one added.
	r.live.set("hello-voicemail", "box/1.wav", []byte("overwritten"))
	r.live.mu.Lock()
	delete(r.live.data, "hello-announcements/welcome.wav")
	r.live.mu.Unlock()
	r.live.set("hello-voicemail", "box/extra.wav", []byte("made after the backup"))

	// Dry run: counts only, nothing written, no maintenance.
	r.tools.onRestore = func() { t.Error("dry run restored") }
	rep, err := s.RestoreRun(ctx, id, "", RestoreOptions{DryRun: true, Actor: "user:a"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Objects != 2 || rep.ObjectsToRestore != 2 || rep.ObjectsMissingInBackup != 0 || rep.SchemaVersion != 5 {
		t.Fatalf("report %+v", rep)
	}
	if len(r.tools.restored) != 0 || len(r.db.audits) != 0 {
		t.Fatal("dry run wrote")
	}

	// A wrong confirmation is refused.
	if _, err := s.RestoreRun(ctx, id, "RESTORE 99", RestoreOptions{Actor: "user:a"}); !errors.Is(err, ErrConfirm) {
		t.Fatalf("confirm: %v", err)
	}

	// The real restore: maintenance during pg_restore, objects back, extras kept.
	r.tools.onRestore = func() {
		if !s.Maintenance.Active(ctx) {
			t.Error("pg_restore ran outside maintenance")
		}
	}
	rep, err = s.RestoreRun(ctx, id, ConfirmText(id), RestoreOptions{Actor: "user:a"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Maintenance.Active(ctx) {
		t.Fatal("still in maintenance")
	}
	if len(r.tools.restored) != 1 || string(r.tools.restored[0]) != "PGDMP-fixture-database" {
		t.Fatalf("restored: %q", r.tools.restored)
	}
	if b, _ := r.live.get("hello-voicemail", "box/1.wav"); string(b) != "voicemail audio" {
		t.Fatalf("changed object not restored: %q", b)
	}
	if b, _ := r.live.get("hello-announcements", "welcome.wav"); string(b) != "welcome announcement" {
		t.Fatalf("missing object not restored: %q", b)
	}
	if b, ok := r.live.get("hello-voicemail", "box/extra.wav"); !ok || string(b) != "made after the backup" {
		t.Fatal("an object absent from the backup was deleted")
	}
	if r.db.bumped != 7 {
		t.Fatalf("revision bump floor = %d", r.db.bumped)
	}
	if len(r.db.audits) != 1 || !strings.HasPrefix(r.db.audits[0], "user:a|restore|backup|1 outcome=ok objects=2 restored=2") {
		t.Fatalf("audit %v", r.db.audits)
	}
	if rep.ObjectsToRestore != 2 {
		t.Fatalf("report %+v", rep)
	}
}

func TestRestoreRefusals(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	if err := s.RunNow(ctx, RunManual); err != nil {
		t.Fatal(err)
	}
	runs, _ := r.db.Runs(ctx, 10)
	key := runs[0].ObjectKey

	// A newer schema than the binary's is refused, and nothing changes.
	old := r.service(t)
	old.BinarySchema = 4
	if _, err := old.Restore(ctx, r.dest, DefaultBucket, key, RestoreOptions{Actor: "x"}); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("newer schema: %v", err)
	}
	// Another key is refused.
	other := r.service(t)
	other.Key = testKey(t)
	if _, err := other.Restore(ctx, r.dest, DefaultBucket, key, RestoreOptions{Actor: "x"}); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("other key: %v", err)
	}
	// A backup store that lost an object is refused before the database is touched.
	op, _ := s.archiveObjects(ctx, r.dest, DefaultBucket, key)
	_ = r.dest.Remove(ctx, DefaultBucket, ObjectsPrefix+op[0].SHA256)
	r.live.set(op[0].Bucket, op[0].Key, []byte("changed"))
	if _, err := s.Restore(ctx, r.dest, DefaultBucket, key, RestoreOptions{Actor: "x"}); !errors.Is(err, ErrVerify) {
		t.Fatalf("missing object: %v", err)
	}
	if len(r.tools.restored) != 0 || len(r.db.audits) != 0 {
		t.Fatal("a refused restore changed state")
	}
	// A failed pg_restore leaves maintenance and is audited.
	_ = r.dest.Remove(ctx, DefaultBucket, key)
	if _, err := s.Restore(ctx, r.dest, DefaultBucket, key, RestoreOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent archive: %v", err)
	}
}

func TestRestoreFailureAudited(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	if err := s.RunNow(ctx, RunManual); err != nil {
		t.Fatal(err)
	}
	runs, _ := r.db.Runs(ctx, 10)
	r.tools.restErr = errors.New("pg_restore: error")
	_, err := s.RestoreRun(ctx, runs[0].ID, ConfirmText(runs[0].ID), RestoreOptions{Actor: "user:a"})
	if err == nil || classOf(err) != "db_restore" {
		t.Fatalf("err %v", err)
	}
	if s.Maintenance.Active(ctx) {
		t.Fatal("maintenance left on")
	}
	if len(r.db.audits) != 1 || !strings.Contains(r.db.audits[0], "outcome=failed") {
		t.Fatalf("audit %v", r.db.audits)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	_ = s.RunNow(ctx, RunManual)
	runs, _ := r.db.Runs(ctx, 10)
	if err := s.Delete(ctx, "user:a", runs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.dest.get(DefaultBucket, runs[0].ObjectKey); ok {
		t.Fatal("archive kept")
	}
	if rs, _ := r.db.Runs(ctx, 10); len(rs) != 0 {
		t.Fatal("row kept")
	}
	if err := s.Delete(ctx, "user:a", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestManualRunBackground(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	run, err := s.StartManual(context.Background())
	if err != nil || run.Status != StatusRunning || run.Kind != RunManual {
		t.Fatalf("start: %v %+v", err, run)
	}
	s.Wait()
	got, _ := r.db.GetRun(context.Background(), run.ID)
	if got.Status != StatusSucceeded {
		t.Fatalf("after: %+v", got)
	}
	// The lock is released.
	if _, err := s.StartManual(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Wait()
}

// A run left "running" by a replica that died is failed under the lock, and
// the schedule then goes on (after the retry wait).
func TestBackupDeadHolder(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	ctx := context.Background()
	if _, err := r.db.InsertRun(ctx, Run{Kind: RunScheduled, Status: StatusRunning, Attempt: 1, StartedAt: r.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	// While its holder is alive (the lock is held), nothing else runs.
	unlock, _, _ := r.db.TryLock(ctx, LockKey)
	_ = s.Tick1(ctx)
	unlock()
	if r.tools.dumps != 0 {
		t.Fatalf("ran beside a live holder: %d", r.tools.dumps)
	}
	// The holder is gone: the row is failed, and the retry follows later.
	_ = s.Tick1(ctx)
	runs, _ := r.db.Runs(ctx, 10)
	if runs[0].Status != StatusFailed || runs[0].ErrorClass != "interrupted" || r.tools.dumps != 0 {
		t.Fatalf("after: %+v dumps=%d", runs[0], r.tools.dumps)
	}
	r.clk.Add(16 * time.Minute)
	_ = s.Tick1(ctx)
	if r.tools.dumps != 1 {
		t.Fatalf("retry: %d", r.tools.dumps)
	}
}
