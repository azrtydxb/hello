package api

// Backup and restore (spec pbx-gaps S-3, S-4): the schedule and destination
// settings, the run history, a manual run, deletion of an archive and the
// restore. All of it is admin-only. The S3 credentials are written, never
// read back. A restore puts the API in maintenance (writes answer 503) until
// it ends; see internal/backup.

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/backup"
)

// BackupStore is the backup settings and history persistence;
// *store.Store implements it.
type BackupStore interface {
	GetBackupSettings(ctx context.Context) (backup.Settings, error)
	PutBackupSettings(ctx context.Context, actor string, in backup.SettingsInput) (backup.Settings, error)
	Runs(ctx context.Context, limit int) ([]backup.Run, error)
}

// BackupRunner starts and restores backups; *backup.Service implements it.
type BackupRunner interface {
	StartManual(ctx context.Context) (backup.Run, error)
	Delete(ctx context.Context, actor string, id int64) error
	RestoreRun(ctx context.Context, id int64, confirm string, opts backup.RestoreOptions) (backup.RestoreReport, error)
}

// MaintenanceGate says whether the API is in maintenance.
type MaintenanceGate interface {
	Active(ctx context.Context) bool
}

func (s *server) backupStore(w http.ResponseWriter) (BackupStore, bool) {
	st, ok := s.Store.(BackupStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backups are not available")
	}
	return st, ok
}

func (s *server) backupRunner(w http.ResponseWriter) (BackupRunner, bool) {
	if s.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backups are not available")
		return nil, false
	}
	return s.Backups, true
}

func (s *server) getBackupSettings(w http.ResponseWriter, r *http.Request) {
	st, ok := s.backupStore(w)
	if !ok {
		return
	}
	v, err := st.GetBackupSettings(r.Context())
	if err != nil {
		s.internal(w, "get backup settings", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) putBackupSettings(w http.ResponseWriter, r *http.Request) {
	st, ok := s.backupStore(w)
	if !ok {
		return
	}
	var in struct {
		Enabled     bool    `json:"enabled"`
		Cron        string  `json:"cron"`
		Kind        string  `json:"kind"`
		S3Endpoint  string  `json:"s3Endpoint"`
		S3Bucket    string  `json:"s3Bucket"`
		S3Region    string  `json:"s3Region"`
		S3PathStyle bool    `json:"s3PathStyle"`
		S3AccessKey *string `json:"s3AccessKey"`
		S3SecretKey *string `json:"s3SecretKey"`
		KeepCount   int     `json:"keepCount"`
		NotifyEmail string  `json:"notifyEmail"`
	}
	if !decode(w, r, &in) {
		return
	}
	input := backup.SettingsInput{Enabled: in.Enabled, Cron: in.Cron, Kind: in.Kind, S3Endpoint: in.S3Endpoint,
		S3Bucket: in.S3Bucket, S3Region: in.S3Region, S3PathStyle: in.S3PathStyle, S3AccessKey: in.S3AccessKey,
		S3SecretKey: in.S3SecretKey, KeepCount: in.KeepCount, NotifyEmail: in.NotifyEmail}
	var f fieldErrs
	for field, msg := range input.Validate() {
		f.add(field, "%s", msg)
	}
	for field, v := range map[string]*string{"s3AccessKey": in.S3AccessKey, "s3SecretKey": in.S3SecretKey} {
		if v != nil && len(*v) > 512 {
			f.add(field, "must be at most 512 characters")
		}
	}
	// Both credentials are replaced together, or neither.
	if (in.S3AccessKey == nil) != (in.S3SecretKey == nil) {
		f.add("s3AccessKey", "send the access key and the secret key together")
	}
	if len(f) == 0 && in.Kind == backup.KindS3 {
		cur, err := st.GetBackupSettings(r.Context())
		if err != nil {
			s.internal(w, "get backup settings", err)
			return
		}
		cleared := in.S3AccessKey != nil && *in.S3AccessKey == "" && in.S3SecretKey != nil && *in.S3SecretKey == ""
		if cleared || (!cur.S3KeysSet && in.S3AccessKey == nil) {
			f.add("s3AccessKey", "an S3 destination needs an access key and a secret key")
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := st.PutBackupSettings(r.Context(), actor(r).String(), input)
	if err != nil {
		s.storeError(w, "backup settings", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) listBackups(w http.ResponseWriter, r *http.Request) {
	st, ok := s.backupStore(w)
	if !ok {
		return
	}
	runs, err := st.Runs(r.Context(), 200)
	if err != nil {
		s.internal(w, "list backups", err)
		return
	}
	writeJSON(w, http.StatusOK, items(runs))
}

// backupError maps the backup package's errors to statuses.
func (s *server) backupError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, backup.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "backup not found")
	case errors.Is(err, backup.ErrBusy):
		writeError(w, http.StatusConflict, "backup_busy", "a backup, or a restore, is already running")
	case errors.Is(err, backup.ErrNewerSchema):
		writeError(w, http.StatusConflict, "schema_newer", "the backup is from a newer version of Hello than the one running")
	case errors.Is(err, backup.ErrKeyMismatch):
		writeError(w, http.StatusConflict, "key_mismatch", "the backup was encrypted under a different HELLO_SECRET_KEY")
	case errors.Is(err, backup.ErrVerify), errors.Is(err, backup.ErrCorrupt):
		writeError(w, http.StatusConflict, "backup_invalid", "the backup failed verification")
	default:
		s.internal(w, what, err)
	}
}

func (s *server) createBackup(w http.ResponseWriter, r *http.Request) {
	run, ok := s.backupRunner(w)
	if !ok {
		return
	}
	// The run belongs to the service, not to this request.
	started, err := run.StartManual(r.Context())
	if err != nil {
		s.backupError(w, "start backup", err)
		return
	}
	if err := s.Store.Audit(r.Context(), actor(r).String(), "run", "backup", strconv.FormatInt(started.ID, 10)); err != nil {
		s.Log.Warn("backup: audit", "error", err)
	}
	writeJSON(w, http.StatusAccepted, started)
}

func (s *server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	run, ok := s.backupRunner(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := run.Delete(r.Context(), actor(r).String(), id); err != nil {
		s.backupError(w, "delete backup", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	run, ok := s.backupRunner(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	dry, err := strconv.ParseBool(r.URL.Query().Get("dry_run"))
	if err != nil && r.URL.Query().Get("dry_run") != "" {
		badRequest(w, "dry_run must be true or false")
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !dry || r.ContentLength != 0 {
		if !decode(w, r, &in) {
			return
		}
	}
	if !dry && in.Confirm != backup.ConfirmText(id) {
		writeFields(w, fieldErrs{{Path: "confirm", Message: `type "` + backup.ConfirmText(id) + `" to confirm`}})
		return
	}
	// The restore outlives a client that gives up: a half-finished restore
	// would be worse than a finished one.
	ctx := context.WithoutCancel(r.Context())
	rep, err := run.RestoreRun(ctx, id, in.Confirm, backup.RestoreOptions{DryRun: dry, Actor: actor(r).String()})
	if err != nil {
		s.backupError(w, "restore backup", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// maintenance answers writes with 503 while a restore runs; reads and the
// health endpoints pass, and SIP is not affected.
func maintenance(g MaintenanceGate, next http.Handler) http.Handler {
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if g.Active(r.Context()) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusServiceUnavailable, "maintenance", "a restore is in progress; changes are refused until it finishes")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
