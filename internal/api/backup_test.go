package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/backup"
)

// bkFake is a Store with the backup settings and runs in memory.
type bkFake struct {
	stubStore
	settings backup.Settings
	keys     [2]string
	runs     []backup.Run
	audits   []string
}

func (f *bkFake) SessionActor(_ context.Context, h []byte) (auth.Actor, error) {
	switch hex.EncodeToString(h) {
	case hex.EncodeToString(auth.HashToken("admin" + cookieSuffix)):
		return auth.Actor{UserID: 1, Username: "admin", Role: auth.RoleAdmin}, nil
	case hex.EncodeToString(auth.HashToken("op" + cookieSuffix)):
		return auth.Actor{UserID: 2, Username: "op", Role: auth.RoleOperator}, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (f *bkFake) Audit(_ context.Context, actor, action, resource, id string) error {
	f.audits = append(f.audits, actor+"|"+action+"|"+resource+"|"+id)
	return nil
}

func (f *bkFake) GetBackupSettings(context.Context) (backup.Settings, error) { return f.settings, nil }

func (f *bkFake) PutBackupSettings(_ context.Context, _ string, in backup.SettingsInput) (backup.Settings, error) {
	f.settings = backup.Settings{Enabled: in.Enabled, Cron: in.Cron, Kind: in.Kind, Bucket: backup.DefaultBucket,
		S3Endpoint: in.S3Endpoint, S3Bucket: in.S3Bucket, S3Region: in.S3Region, S3PathStyle: in.S3PathStyle,
		S3KeysSet: f.settings.S3KeysSet, KeepCount: in.KeepCount, NotifyEmail: in.NotifyEmail, UpdatedAt: time.Now()}
	if in.S3AccessKey != nil {
		f.keys = [2]string{*in.S3AccessKey, *in.S3SecretKey}
		f.settings.S3KeysSet = *in.S3AccessKey != ""
	}
	return f.settings, nil
}

func (f *bkFake) Runs(context.Context, int) ([]backup.Run, error) { return f.runs, nil }

type bkRunner struct {
	started int
	busy    bool
	deleted []int64
	dry     []bool
	err     error
	report  backup.RestoreReport
}

func (r *bkRunner) StartManual(context.Context) (backup.Run, error) {
	if r.busy {
		return backup.Run{}, backup.ErrBusy
	}
	r.started++
	return backup.Run{ID: 9, Kind: backup.RunManual, Status: backup.StatusRunning, Attempt: 1, StartedAt: time.Now()}, nil
}

func (r *bkRunner) Delete(_ context.Context, _ string, id int64) error {
	r.deleted = append(r.deleted, id)
	return r.err
}

func (r *bkRunner) RestoreRun(_ context.Context, id int64, confirm string, o backup.RestoreOptions) (backup.RestoreReport, error) {
	r.dry = append(r.dry, o.DryRun)
	if r.err != nil {
		return backup.RestoreReport{}, r.err
	}
	return r.report, nil
}

type gate struct{ on atomic.Bool }

func (g *gate) Active(context.Context) bool { return g.on.Load() }

func bkCall(h http.Handler, who, method, path string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	if body == nil {
		r.ContentLength = 0
	}
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func bkRig() (*bkFake, *bkRunner, *gate, http.Handler) {
	f := &bkFake{settings: backup.DefaultSettings()}
	r := &bkRunner{report: backup.RestoreReport{DryRun: true, HelloVersion: "v", SchemaVersion: 45, CreatedAt: time.Now(), Objects: 3}}
	g := &gate{}
	return f, r, g, Handler(Config{Store: f, Backups: r, Maintenance: g, SessionTTL: time.Hour})
}

func TestBackupSettingsAPI(t *testing.T) {
	f, _, _, h := bkRig()
	if rec := bkCall(h, "op", "GET", "/api/v1/backups/settings", nil); rec.Code != 403 {
		t.Fatalf("operator read: %d", rec.Code)
	}
	got := wantJSON(t, bkCall(h, "admin", "GET", "/api/v1/backups/settings", nil), 200, "get")
	if got["cron"] != "0 2 * * *" || got["keepCount"] != float64(14) || got["kind"] != "minio" || got["bucket"] != "hello-backups" {
		t.Fatalf("defaults: %v", got)
	}
	// Bad input names its fields.
	rec := bkCall(h, "admin", "PUT", "/api/v1/backups/settings", map[string]any{"enabled": true, "cron": "nope", "kind": "s3", "keepCount": 0})
	if rec.Code != 400 {
		t.Fatalf("bad put: %d %s", rec.Code, rec.Body)
	}
	for _, f := range []string{"cron", "keepCount", "s3Endpoint", "s3Bucket"} {
		if !strings.Contains(rec.Body.String(), `"`+f+`"`) {
			t.Errorf("no error for %s: %s", f, rec.Body)
		}
	}
	// An S3 destination needs credentials, and they come as a pair.
	s3 := map[string]any{"enabled": true, "cron": "30 3 * * *", "kind": "s3", "s3Endpoint": "https://s3.example.com", "s3Bucket": "b", "keepCount": 7}
	if rec := bkCall(h, "admin", "PUT", "/api/v1/backups/settings", s3); rec.Code != 400 {
		t.Fatalf("s3 without keys: %d", rec.Code)
	}
	s3["s3AccessKey"] = "AK"
	if rec := bkCall(h, "admin", "PUT", "/api/v1/backups/settings", s3); rec.Code != 400 {
		t.Fatalf("one key only: %d", rec.Code)
	}
	secretValue := "secret-value-for-test"
	s3["s3SecretKey"] = secretValue
	rec = bkCall(h, "admin", "PUT", "/api/v1/backups/settings", s3)
	got = wantJSON(t, rec, 200, "put s3")
	if got["s3KeysSet"] != true || strings.Contains(rec.Body.String(), secretValue) || f.keys[1] != secretValue {
		t.Fatalf("keys: %v / %s", got, rec.Body)
	}
	if rec := bkCall(h, "admin", "GET", "/api/v1/backups/settings", nil); strings.Contains(rec.Body.String(), secretValue) {
		t.Fatal("secret returned")
	}
	// Omitting the keys keeps them.
	delete(s3, "s3AccessKey")
	delete(s3, "s3SecretKey")
	got = wantJSON(t, bkCall(h, "admin", "PUT", "/api/v1/backups/settings", s3), 200, "keep keys")
	if got["s3KeysSet"] != true || f.keys[0] != "AK" {
		t.Fatalf("keys dropped: %v", got)
	}
}

func TestBackupRunsAPI(t *testing.T) {
	f, r, _, h := bkRig()
	f.runs = []backup.Run{{ID: 1, Kind: backup.RunScheduled, Status: backup.StatusSucceeded, Attempt: 1, StartedAt: time.Now(), ObjectKey: "archives/x.hbk"}}
	if items := nextOf(wantJSON(t, bkCall(h, "admin", "GET", "/api/v1/backups", nil), 200, "list")); len(items) != 1 {
		t.Fatalf("items: %v", items)
	}
	if rec := bkCall(h, "op", "POST", "/api/v1/backups", nil); rec.Code != 403 {
		t.Fatalf("operator start: %d", rec.Code)
	}
	got := wantJSON(t, bkCall(h, "admin", "POST", "/api/v1/backups", nil), 202, "start")
	if got["status"] != "running" || r.started != 1 || len(f.audits) != 1 || !strings.HasSuffix(f.audits[0], "|run|backup|9") {
		t.Fatalf("start: %v %d %v", got, r.started, f.audits)
	}
	r.busy = true
	if rec := bkCall(h, "admin", "POST", "/api/v1/backups", nil); rec.Code != 409 || acctErrCode(rec) != "backup_busy" {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}
	if rec := bkCall(h, "admin", "DELETE", "/api/v1/backups/1", nil); rec.Code != 204 || len(r.deleted) != 1 {
		t.Fatalf("delete: %d", rec.Code)
	}
	r.err = backup.ErrNotFound
	if rec := bkCall(h, "admin", "DELETE", "/api/v1/backups/5", nil); rec.Code != 404 {
		t.Fatalf("delete missing: %d", rec.Code)
	}
	r.err = backup.ErrBusy
	if rec := bkCall(h, "admin", "DELETE", "/api/v1/backups/6", nil); rec.Code != 409 || acctErrCode(rec) != "backup_busy" {
		t.Fatalf("delete running: %d", rec.Code)
	}
}

func TestBackupRestoreAPI(t *testing.T) {
	_, r, _, h := bkRig()
	// A dry run needs no confirmation and no body, and returns counts only.
	got := wantJSON(t, bkCall(h, "admin", "POST", "/api/v1/backups/3/restore?dry_run=true", nil), 200, "dry run")
	if got["objects"] != float64(3) || len(r.dry) != 1 || !r.dry[0] {
		t.Fatalf("dry: %v %v", got, r.dry)
	}
	// A real restore needs the typed confirmation.
	for _, body := range []any{nil, map[string]any{"confirm": "RESTORE 4"}, map[string]any{"confirm": "restore 3"}} {
		if rec := bkCall(h, "admin", "POST", "/api/v1/backups/3/restore", body); rec.Code != 400 {
			t.Fatalf("unconfirmed %v: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(r.dry) != 1 {
		t.Fatal("an unconfirmed restore ran")
	}
	wantJSON(t, bkCall(h, "admin", "POST", "/api/v1/backups/3/restore", map[string]any{"confirm": "RESTORE 3"}), 200, "restore")
	if len(r.dry) != 2 || r.dry[1] {
		t.Fatalf("restore calls: %v", r.dry)
	}
	if rec := bkCall(h, "op", "POST", "/api/v1/backups/3/restore", map[string]any{"confirm": "RESTORE 3"}); rec.Code != 403 {
		t.Fatalf("operator restore: %d", rec.Code)
	}
	for err, want := range map[error][2]any{
		backup.ErrNewerSchema: {409, "schema_newer"}, backup.ErrKeyMismatch: {409, "key_mismatch"},
		backup.ErrVerify: {409, "backup_invalid"}, backup.ErrNotFound: {404, "not_found"}, backup.ErrBusy: {409, "backup_busy"},
	} {
		r.err = err
		rec := bkCall(h, "admin", "POST", "/api/v1/backups/3/restore?dry_run=true", nil)
		if rec.Code != want[0] || acctErrCode(rec) != want[1] {
			t.Errorf("%v: %d %s", err, rec.Code, rec.Body)
		}
	}
}

func TestMaintenanceRefusesWrites(t *testing.T) {
	_, _, g, h := bkRig()
	g.on.Store(true)
	// Reads pass; every write is refused with 503 maintenance.
	if rec := bkCall(h, "admin", "GET", "/api/v1/backups/settings", nil); rec.Code != 200 {
		t.Fatalf("read in maintenance: %d", rec.Code)
	}
	for _, m := range [][2]string{{"PUT", "/api/v1/backups/settings"}, {"POST", "/api/v1/extensions"}, {"DELETE", "/api/v1/extensions/1"}, {"POST", "/api/v1/auth/login"}} {
		rec := bkCall(h, "admin", m[0], m[1], map[string]any{})
		if rec.Code != 503 || acctErrCode(rec) != "maintenance" || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s %s: %d %s", m[0], m[1], rec.Code, rec.Body)
		}
	}
	g.on.Store(false)
	if rec := bkCall(h, "admin", "PUT", "/api/v1/backups/settings", map[string]any{"enabled": true, "cron": "0 2 * * *", "kind": "minio", "keepCount": 14}); rec.Code != 200 {
		t.Fatalf("after maintenance: %d %s", rec.Code, rec.Body)
	}
}
