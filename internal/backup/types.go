package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
)

// Destination kinds, defaults and limits (spec D2, S-3).
const (
	KindMinio     = "minio"
	KindS3        = "s3"
	DefaultBucket = "hello-backups"
	DefaultCron   = "0 2 * * *"
	DefaultKeep   = 14
	MaxKeep       = 365

	ArchivePrefix = "archives/"
	ObjectsPrefix = "objects/"
)

// Run kinds and statuses.
const (
	RunScheduled = "scheduled"
	RunManual    = "manual"

	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Errors the API maps to statuses.
var (
	ErrBusy        = errors.New("backup: a backup or restore is already running")
	ErrNotFound    = errors.New("backup: not found")
	ErrNewerSchema = errors.New("backup: the archive's schema is newer than this version")
	ErrVerify      = errors.New("backup: verification failed")
	ErrConfirm     = errors.New("backup: confirmation text does not match")
)

// Settings is the singleton backup configuration without secrets.
type Settings struct {
	Enabled     bool      `json:"enabled"`
	Cron        string    `json:"cron"`
	Kind        string    `json:"kind"`
	Bucket      string    `json:"bucket"`
	S3Endpoint  string    `json:"s3Endpoint"`
	S3Bucket    string    `json:"s3Bucket"`
	S3Region    string    `json:"s3Region"`
	S3PathStyle bool      `json:"s3PathStyle"`
	S3KeysSet   bool      `json:"s3KeysSet"`
	KeepCount   int       `json:"keepCount"`
	NotifyEmail string    `json:"notifyEmail"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// SecretSettings are the settings with the S3 credentials in clear, for the
// runner only.
type SecretSettings struct {
	Settings
	S3AccessKey, S3SecretKey string
}

// DefaultSettings are what a deployment that never saved any gets.
func DefaultSettings() Settings {
	return Settings{Enabled: true, Cron: DefaultCron, Kind: KindMinio, Bucket: DefaultBucket, KeepCount: DefaultKeep}
}

// SettingsInput is an update. A nil key pointer keeps the stored
// credentials; both pointing at "" clears them.
type SettingsInput struct {
	Enabled     bool
	Cron        string
	Kind        string
	S3Endpoint  string
	S3Bucket    string
	S3Region    string
	S3PathStyle bool
	S3AccessKey *string
	S3SecretKey *string
	KeepCount   int
	NotifyEmail string
}

// Validate returns field to message pairs.
func (in SettingsInput) Validate() map[string]string {
	bad := map[string]string{}
	if _, err := ParseSchedule(in.Cron); err != nil {
		bad["cron"] = err.Error()
	}
	if in.KeepCount < 1 || in.KeepCount > MaxKeep {
		bad["keepCount"] = fmt.Sprintf("must be between 1 and %d", MaxKeep)
	}
	if in.NotifyEmail != "" && !mailer.ValidAddress(in.NotifyEmail) {
		bad["notifyEmail"] = "must be an email address"
	}
	switch in.Kind {
	case KindMinio:
	case KindS3:
		if _, _, err := ParseEndpoint(in.S3Endpoint); err != nil {
			bad["s3Endpoint"] = err.Error()
		}
		if in.S3Bucket == "" || len(in.S3Bucket) > 255 {
			bad["s3Bucket"] = "is required"
		}
		if len(in.S3Region) > 64 {
			bad["s3Region"] = "is too long"
		}
	default:
		bad["kind"] = "must be minio or s3"
	}
	return bad
}

// Run is one backup run.
type Run struct {
	ID           int64      `json:"id"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	Attempt      int        `json:"attempt"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	DBBytes      int64      `json:"dbBytes"`
	ObjectCount  int        `json:"objectCount"`
	ObjectBytes  int64      `json:"objectBytes"`
	ArchiveBytes int64      `json:"archiveBytes"`
	ObjectKey    string     `json:"objectKey"`
	ErrorClass   string     `json:"errorClass"`
}

// DB is the persistence a Service needs; *store.Store implements it.
type DB interface {
	BackupSettings(ctx context.Context) (SecretSettings, error)
	InsertRun(ctx context.Context, r Run) (int64, error)
	FinishRun(ctx context.Context, r Run) error
	// FailRunning marks every running row failed with class "interrupted".
	FailRunning(ctx context.Context, at time.Time) error
	Runs(ctx context.Context, limit int) ([]Run, error)
	GetRun(ctx context.Context, id int64) (Run, error)
	DeleteRun(ctx context.Context, id int64) error
	DeleteRunByKey(ctx context.Context, key string) error
	// ScheduledSince lists scheduled runs started at or after t, newest first.
	ScheduledSince(ctx context.Context, t time.Time) ([]Run, error)
	LastSuccess(ctx context.Context) (time.Time, bool, error)
	// AdoptArchive records an archive present in the destination but absent
	// from the table (after a restore). A row for the key that a dump caught
	// still running is completed; any other existing row is left alone.
	AdoptArchive(ctx context.Context, key string, size int64, at time.Time) error
	SchemaVersion(ctx context.Context) (int64, error)
	ConfigRevision(ctx context.Context) (int64, error)
	BumpRevision(ctx context.Context, atLeast int64) error
	Audit(ctx context.Context, actor, action, resource, id string) error
	TryLock(ctx context.Context, key string) (func(), bool, error)
}
