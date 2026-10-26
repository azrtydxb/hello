package main

// Backup and restore wiring (spec pbx-gaps S-2 to S-4): the scheduler and
// the API service in serve, and the "backup restore" command for when the
// API is down.

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/backup"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/version"
)

const maintenanceKey = "hello:maintenance"

// maintenanceTTL bounds a flag left behind by a node that died mid-restore.
const maintenanceTTL = time.Hour

// vkFlag is the cluster-wide maintenance flag in Valkey.
type vkFlag struct{ vk *api.LazyValkey }

func (f vkFlag) Set(ctx context.Context, on bool) error {
	c := f.vk.Client()
	if c == nil {
		return errors.New("valkey is not connected")
	}
	if on {
		return c.Do(ctx, c.B().Set().Key(maintenanceKey).Value("1").ExSeconds(int64(maintenanceTTL.Seconds())).Build()).Error()
	}
	return c.Do(ctx, c.B().Del().Key(maintenanceKey).Build()).Error()
}

func (f vkFlag) Get(ctx context.Context) (bool, error) {
	c := f.vk.Client()
	if c == nil {
		return false, errors.New("valkey is not connected")
	}
	err := c.Do(ctx, c.B().Get().Key(maintenanceKey).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	return err == nil, err
}

// backupDest builds the destination from the settings: the bundled MinIO
// bucket, or an external S3 service.
func backupDest(live backup.Store) backup.DestFunc {
	return func(_ context.Context, s backup.SecretSettings) (backup.Store, string, error) {
		if s.Kind != backup.KindS3 {
			return live, backup.DefaultBucket, nil
		}
		host, secure, err := backup.ParseEndpoint(s.S3Endpoint)
		if err != nil {
			return nil, "", err
		}
		st, err := backup.NewMinio(host, s.S3AccessKey, s.S3SecretKey, secure, s.S3Region, s.S3PathStyle)
		return st, s.S3Bucket, err
	}
}

// newBackupService builds the service the scheduler and the API share.
func newBackupService(cfg config.Control, st *store.Store, db *sql.DB, vk *api.LazyValkey, reg *prometheus.Registry, log *slog.Logger) (*backup.Service, error) {
	key, err := backup.DeriveKey(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	live, err := backup.NewMinio(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioSecure, "", false)
	if err != nil {
		return nil, err
	}
	var gauge prometheus.Gauge
	if reg != nil {
		gauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_backup_last_success_timestamp",
			Help: "Unix time the last successful backup finished; 0 when none did. Alert on time() minus this.",
		})
		reg.MustRegister(gauge)
	}
	env := envSMTP(cfg)
	var shared backup.SharedFlag
	if vk != nil {
		shared = vkFlag{vk}
	}
	return backup.New(backup.Config{
		DB: st, Key: key, Live: live, Dest: backupDest(live),
		Tools:        backup.PGTools{DumpBin: cfg.Backup.PGDump, RestoreBin: cfg.Backup.PGRestore, Conn: cfg.Database},
		Migrate:      func(ctx context.Context) error { _, err := migrate.Up(ctx, db); return err },
		Maintenance:  &backup.Maintenance{Shared: shared},
		Tmp:          cfg.Backup.Tmp,
		HelloVersion: version.Version,
		Log:          log, Metric: gauge,
		Notify: func(ctx context.Context, to, subject, body string) error {
			s, source, err := api.EffectiveSMTP(ctx, st, env)
			if err != nil {
				return err
			}
			if source == api.SMTPNone {
				return errors.New("smtp is not configured")
			}
			msg, err := mailer.BuildNotice(s.From, to, subject, body, time.Now())
			if err != nil {
				return err
			}
			return mailer.Deliver(ctx, s, to, msg)
		},
	}), nil
}

const backupUsage = "usage: hello-control backup restore --from <object-key> [--bucket B] [--s3-endpoint URL [--s3-region R] [--s3-path-style]] [--dry-run] [--yes]"

// backupRestore is "hello-control backup restore": restore an archive from
// the command line, for when the API is down. HELLO_SECRET_KEY must be the
// key the archive was written under. The destination is the bundled MinIO
// bucket hello-backups, or an S3 service named by --s3-endpoint with its
// credentials in HELLO_BACKUP_S3_ACCESS_KEY and HELLO_BACKUP_S3_SECRET_KEY.
func backupRestore(ctx context.Context, cfg config.Control, db *sql.DB, log *slog.Logger, args []string, getenv func(string) string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "")
	bucket := fs.String("bucket", backup.DefaultBucket, "")
	s3 := fs.String("s3-endpoint", "", "")
	region := fs.String("s3-region", "", "")
	pathStyle := fs.Bool("s3-path-style", false, "")
	dry := fs.Bool("dry-run", false, "")
	yes := fs.Bool("yes", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *from == "" {
		return errors.New(backupUsage)
	}
	st := store.New(db)
	svc, err := newBackupService(cfg, st, db, nil, nil, log)
	if err != nil {
		return err
	}
	var dest backup.Store
	if *s3 != "" {
		host, secure, err := backup.ParseEndpoint(*s3)
		if err != nil {
			return fmt.Errorf("--s3-endpoint: %w", err)
		}
		dest, err = backup.NewMinio(host, getenv("HELLO_BACKUP_S3_ACCESS_KEY"), getenv("HELLO_BACKUP_S3_SECRET_KEY"), secure, *region, *pathStyle)
		if err != nil {
			return err
		}
	} else if dest, err = backup.NewMinio(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioSecure, "", false); err != nil {
		return err
	}
	if !*dry && !*yes {
		_, _ = fmt.Fprintf(out, "This replaces the database and any changed objects with the backup %s.\nType RESTORE to continue: ", *from)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != "RESTORE" {
			return errors.New("not confirmed; nothing was changed")
		}
	}
	rep, err := svc.Restore(ctx, dest, *bucket, *from, backup.RestoreOptions{DryRun: *dry, Actor: "cli"})
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	verb := "restored"
	if *dry {
		verb = "verified (dry run, nothing written)"
	}
	_, _ = fmt.Fprintf(out, "%s: schema %d, created %s, database %d bytes, %d objects (%d to restore, %d missing from the backup)\n",
		verb, rep.SchemaVersion, rep.CreatedAt.Format(time.RFC3339), rep.DBBytes, rep.Objects, rep.ObjectsToRestore, rep.ObjectsMissingInBackup)
	return nil
}
