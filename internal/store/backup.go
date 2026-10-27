package store

// Backup settings and run history (spec pbx-gaps S-3). The S3 credentials of
// an external destination are sealed under the AAD "backup:s3"; only the
// backup runner reads them back, and no API response carries them.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/backup"
)

const backupS3AAD = "backup:s3"

type s3Keys struct {
	Access string `json:"a"`
	Secret string `json:"s"`
}

// GetBackupSettings returns the settings without credentials; a deployment
// that never saved any gets the defaults.
func (s *Store) GetBackupSettings(ctx context.Context) (backup.Settings, error) {
	v := backup.DefaultSettings()
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, cron, kind, s3_endpoint, s3_bucket, s3_region, s3_path_style,
		       s3_keys_sealed IS NOT NULL, keep_count, notify_email, updated_at
		FROM backup_settings`).Scan(&v.Enabled, &v.Cron, &v.Kind, &v.S3Endpoint, &v.S3Bucket, &v.S3Region,
		&v.S3PathStyle, &v.S3KeysSet, &v.KeepCount, &v.NotifyEmail, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return backup.DefaultSettings(), nil
	}
	return v, err
}

// BackupSettings returns the settings with the S3 credentials opened, for the
// runner.
func (s *Store) BackupSettings(ctx context.Context) (backup.SecretSettings, error) {
	v, err := s.GetBackupSettings(ctx)
	out := backup.SecretSettings{Settings: v}
	if err != nil || !v.S3KeysSet {
		return out, err
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT s3_keys_sealed FROM backup_settings`).Scan(&sealed); err != nil {
		return out, err
	}
	if s.box == nil {
		return out, errors.New("store: no secret box to open the backup credentials")
	}
	raw, err := s.box.Open(sealed, backupS3AAD)
	if err != nil {
		return out, err
	}
	var k s3Keys
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return out, err
	}
	out.S3AccessKey, out.S3SecretKey = k.Access, k.Secret
	return out, nil
}

// PutBackupSettings saves the settings and audits the change (never the
// credentials). Nil key pointers keep the stored credentials; both set to ""
// clear them.
func (s *Store) PutBackupSettings(ctx context.Context, actor string, in backup.SettingsInput) (backup.Settings, error) {
	var sealed []byte
	clearKeys := false
	if in.S3AccessKey != nil || in.S3SecretKey != nil {
		var a, sec string
		if in.S3AccessKey != nil {
			a = *in.S3AccessKey
		}
		if in.S3SecretKey != nil {
			sec = *in.S3SecretKey
		}
		if a == "" && sec == "" {
			clearKeys = true
		} else {
			if s.box == nil {
				return backup.Settings{}, errors.New("store: no secret box to seal the backup credentials")
			}
			b, err := json.Marshal(s3Keys{Access: a, Secret: sec}) //nolint:gosec // G117: sealed with the secret box before it is stored
			if err != nil {
				return backup.Settings{}, err
			}
			if sealed, err = s.box.Seal(string(b), backupS3AAD); err != nil {
				return backup.Settings{}, err
			}
		}
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO backup_settings (id, enabled, cron, kind, s3_endpoint, s3_bucket, s3_region, s3_path_style,
			                             s3_keys_sealed, keep_count, notify_email)
			VALUES (TRUE, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (id) DO UPDATE SET enabled = $1, cron = $2, kind = $3, s3_endpoint = $4, s3_bucket = $5,
				s3_region = $6, s3_path_style = $7, keep_count = $9, notify_email = $10, updated_at = now(),
				s3_keys_sealed = CASE WHEN $11::boolean THEN NULL WHEN $8::bytea IS NOT NULL THEN $8::bytea
				                      ELSE backup_settings.s3_keys_sealed END`,
			in.Enabled, in.Cron, in.Kind, in.S3Endpoint, in.S3Bucket, in.S3Region, in.S3PathStyle,
			sealed, in.KeepCount, in.NotifyEmail, clearKeys); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "backup_settings", "1")
	})
	if err != nil {
		return backup.Settings{}, err
	}
	return s.GetBackupSettings(ctx)
}

const runCols = `id, kind, status, attempt, started_at, finished_at, db_bytes, object_count, object_bytes,
	archive_bytes, object_key, error_class`

func scanRun(sc interface{ Scan(...any) error }) (backup.Run, error) {
	var r backup.Run
	var fin sql.NullTime
	err := sc.Scan(&r.ID, &r.Kind, &r.Status, &r.Attempt, &r.StartedAt, &fin, &r.DBBytes, &r.ObjectCount,
		&r.ObjectBytes, &r.ArchiveBytes, &r.ObjectKey, &r.ErrorClass)
	if fin.Valid {
		t := fin.Time
		r.FinishedAt = &t
	}
	return r, err
}

// InsertRun starts a run row.
func (s *Store) InsertRun(ctx context.Context, r backup.Run) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO backup_runs (kind, status, attempt, started_at, object_key)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, r.Kind, r.Status, r.Attempt, r.StartedAt, r.ObjectKey).Scan(&id)
	return id, err
}

// FinishRun records a run's outcome.
func (s *Store) FinishRun(ctx context.Context, r backup.Run) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET status = $2, finished_at = $3, db_bytes = $4, object_count = $5, object_bytes = $6,
			archive_bytes = $7, error_class = $8
		WHERE id = $1`, r.ID, r.Status, r.FinishedAt, r.DBBytes, r.ObjectCount, r.ObjectBytes, r.ArchiveBytes, r.ErrorClass)
	return err
}

// FailRunning marks every running row failed (its holder died).
func (s *Store) FailRunning(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET status = 'failed', error_class = 'interrupted', finished_at = $1
		WHERE status = 'running'`, at)
	return err
}

// Runs lists runs, newest first.
func (s *Store) Runs(ctx context.Context, limit int) ([]backup.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []backup.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun returns one run, backup.ErrNotFound when absent.
func (s *Store) GetRun(ctx context.Context, id int64) (backup.Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM backup_runs WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return backup.Run{}, backup.ErrNotFound
	}
	return r, err
}

// DeleteRun removes a run row.
func (s *Store) DeleteRun(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM backup_runs WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return backup.ErrNotFound
	}
	return nil
}

// DeleteRunByKey removes the rows of an archive that retention removed.
func (s *Store) DeleteRunByKey(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_runs WHERE object_key = $1`, key)
	return err
}

// ScheduledSince lists scheduled runs started at or after t, newest first.
func (s *Store) ScheduledSince(ctx context.Context, t time.Time) ([]backup.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM backup_runs
		WHERE kind = 'scheduled' AND started_at >= $1 ORDER BY started_at DESC, id DESC`, t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []backup.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastSuccess is when the latest successful run finished.
func (s *Store) LastSuccess(ctx context.Context) (time.Time, bool, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT max(finished_at) FROM backup_runs WHERE status = 'succeeded'`).Scan(&t)
	return t.Time, t.Valid, err
}

// AdoptArchive records an archive the destination holds but the table does
// not; a row a dump caught still running is completed.
func (s *Store) AdoptArchive(ctx context.Context, key string, size int64, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE backup_runs SET status = 'succeeded', archive_bytes = $2, finished_at = COALESCE(finished_at, $3)
			WHERE object_key = $1 AND status = 'running'`, key, size, at)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO backup_runs (kind, status, started_at, finished_at, archive_bytes, object_key)
			SELECT 'scheduled', 'succeeded', $3, $3, $2, $1
			WHERE NOT EXISTS (SELECT 1 FROM backup_runs WHERE object_key = $1)`, key, size, at)
		return err
	})
}

// SchemaVersion is the highest applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v)
	return v.Int64, err
}

// BumpRevision raises the configuration revision above atLeast and announces
// it, so every node rebuilds its snapshot from the restored data even when
// the restored revision was lower than the one they hold.
func (s *Store) BumpRevision(ctx context.Context, atLeast int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var rev int64
		if err := tx.QueryRowContext(ctx,
			`UPDATE schema_info SET config_revision = GREATEST(config_revision, $1::bigint) + 1 RETURNING config_revision`,
			atLeast).Scan(&rev); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, NotifyChannel, itoa(rev))
		return err
	})
}
