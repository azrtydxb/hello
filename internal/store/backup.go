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

	"github.com/azrtydxb/momo"

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
	err := s.tx(ctx, func(tx *dbTx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO backup_settings (id, enabled, cron, kind, s3_endpoint, s3_bucket, s3_region, s3_path_style,
			                             s3_keys_sealed, keep_count, notify_email)
			VALUES (TRUE, @enabled, @cron, @kind, @s3_endpoint, @s3_bucket, @s3_region, @s3_path_style, @s3_keys_sealed, @keep_count, @notify_email)
			ON CONFLICT (id) DO UPDATE SET enabled = @enabled, cron = @cron, kind = @kind, s3_endpoint = @s3_endpoint, s3_bucket = @s3_bucket,
				s3_region = @s3_region, s3_path_style = @s3_path_style, keep_count = @keep_count, notify_email = @notify_email, updated_at = now(),
				s3_keys_sealed = CASE WHEN @clear_keys::boolean THEN NULL WHEN @s3_keys_sealed::bytea IS NOT NULL THEN @s3_keys_sealed::bytea
				                      ELSE backup_settings.s3_keys_sealed END`,
			momo.Named("enabled", in.Enabled), momo.Named("cron", in.Cron), momo.Named("kind", in.Kind),
			momo.Named("s3_endpoint", in.S3Endpoint), momo.Named("s3_bucket", in.S3Bucket), momo.Named("s3_region", in.S3Region),
			momo.Named("s3_path_style", in.S3PathStyle), momo.Named("s3_keys_sealed", sealed), momo.Named("keep_count", in.KeepCount),
			momo.Named("notify_email", in.NotifyEmail), momo.Named("clear_keys", clearKeys)); err != nil {
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
		VALUES (@kind, @status, @attempt, @started_at, @object_key) RETURNING id`,
		momo.Named("kind", r.Kind), momo.Named("status", r.Status), momo.Named("attempt", r.Attempt),
		momo.Named("started_at", r.StartedAt), momo.Named("object_key", r.ObjectKey)).Scan(&id)
	return id, err
}

// FinishRun records a run's outcome.
func (s *Store) FinishRun(ctx context.Context, r backup.Run) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET status = @status, finished_at = @finished_at, db_bytes = @db_bytes, object_count = @object_count,
			object_bytes = @object_bytes, archive_bytes = @archive_bytes, error_class = @error_class
		WHERE id = @id`, momo.Named("id", r.ID), momo.Named("status", r.Status), momo.Named("finished_at", r.FinishedAt),
		momo.Named("db_bytes", r.DBBytes), momo.Named("object_count", r.ObjectCount), momo.Named("object_bytes", r.ObjectBytes),
		momo.Named("archive_bytes", r.ArchiveBytes), momo.Named("error_class", r.ErrorClass))
	return err
}

// FailRunning marks every running row failed (its holder died).
func (s *Store) FailRunning(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET status = 'failed', error_class = 'interrupted', finished_at = @finished_at
		WHERE status = 'running'`, momo.Named("finished_at", at))
	return err
}

// Runs lists runs, newest first.
func (s *Store) Runs(ctx context.Context, limit int) ([]backup.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT @limit`, momo.Named("limit", limit))
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
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM backup_runs WHERE id = @id`, momo.Named("id", id)))
	if errors.Is(err, sql.ErrNoRows) {
		return backup.Run{}, backup.ErrNotFound
	}
	return r, err
}

// DeleteRun removes a run row.
func (s *Store) DeleteRun(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM backup_runs WHERE id = @id`, momo.Named("id", id))
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_runs WHERE object_key = @object_key`, momo.Named("object_key", key))
	return err
}

// ScheduledSince lists scheduled runs started at or after t, newest first.
func (s *Store) ScheduledSince(ctx context.Context, t time.Time) ([]backup.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM backup_runs
		WHERE kind = 'scheduled' AND started_at >= @since ORDER BY started_at DESC, id DESC`, momo.Named("since", t))
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
	return s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE backup_runs SET status = 'succeeded', archive_bytes = @archive_bytes, finished_at = COALESCE(finished_at, @at)
			WHERE object_key = @object_key AND status = 'running'`,
			momo.Named("object_key", key), momo.Named("archive_bytes", size), momo.Named("at", at))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO backup_runs (kind, status, started_at, finished_at, archive_bytes, object_key)
			SELECT 'scheduled', 'succeeded', @at::timestamptz, @at::timestamptz, @archive_bytes::bigint, @object_key::text
			WHERE NOT EXISTS (SELECT 1 FROM backup_runs WHERE object_key = @object_key::text)`,
			momo.Named("object_key", key), momo.Named("archive_bytes", size), momo.Named("at", at))
		return err
	})
}

// SchemaVersion is the newest applied migration: momo's 14-digit version
// from momo_migrations, or on a database goose built that was not taken over
// yet (internal/migrate), goose's newest version. The history tables belong to
// the migration engines, so the read stays raw database/sql and binds
// nothing.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	var momoTable, gooseTable bool
	if err := s.db.raw.QueryRowContext(ctx,
		`SELECT to_regclass('momo_migrations') IS NOT NULL, to_regclass('goose_db_version') IS NOT NULL`,
	).Scan(&momoTable, &gooseTable); err != nil {
		return 0, err
	}
	var v sql.NullInt64
	if momoTable {
		if err := s.db.raw.QueryRowContext(ctx,
			`SELECT max(version::bigint) FROM momo_migrations WHERE status = 'applied' AND version ~ '^[0-9]{14}$'`,
		).Scan(&v); err != nil || v.Valid {
			return v.Int64, err
		}
	}
	if gooseTable {
		err := s.db.raw.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v)
		return v.Int64, err
	}
	return 0, nil
}

// BumpRevision raises the configuration revision above atLeast and announces
// it, so every node rebuilds its snapshot from the restored data even when
// the restored revision was lower than the one they hold.
func (s *Store) BumpRevision(ctx context.Context, atLeast int64) error {
	return s.tx(ctx, func(tx *dbTx) error {
		var rev int64
		if err := tx.QueryRowContext(ctx,
			`UPDATE schema_info SET config_revision = GREATEST(config_revision, @at_least::bigint) + 1 RETURNING config_revision`,
			momo.Named("at_least", atLeast)).Scan(&rev); err != nil {
			return err
		}
		return notifyConfig(ctx, tx, rev)
	})
}
