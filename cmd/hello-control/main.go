// Command hello-control is Hello's management and control-plane service.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
	"github.com/azrtydxb/hello/internal/vkconn"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	// bootstrapRetry is how often serve retries creating the bootstrap admin
	// while the database is unreachable or not yet migrated.
	bootstrapRetry = 5 * time.Second
	// pruneEvery is how often expired sessions are deleted.
	pruneEvery = time.Hour
)

const usage = "usage: hello-control serve | migrate up | migrate status"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hello-control:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.LoadControl(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := telemetry.NewLogger(os.Stderr, cfg.LogLevel, "hello-control", cfg.NodeID)

	// OpenDB does not connect; an unreachable database surfaces in
	// readiness (serve) or as the command's error (migrate).
	db := stdlib.OpenDB(*cfg.Database)
	defer func() { _ = db.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch {
	case len(args) == 1 && args[0] == "serve":
		return serve(ctx, cfg, log, db)
	case len(args) == 2 && args[0] == "migrate" && args[1] == "up":
		n, err := migrate.Up(ctx, db)
		if err != nil {
			return err
		}
		log.Info("migrations applied", "count", n)
		return nil
	case len(args) == 2 && args[0] == "migrate" && args[1] == "status":
		st, err := migrate.Status(ctx, db)
		if err != nil {
			return err
		}
		for _, s := range st {
			fmt.Printf("%05d %-8s %s\n", s.Source.Version, s.State, s.Source.Path)
		}
		return nil
	}
	return fmt.Errorf("%s", usage)
}

func serve(ctx context.Context, cfg config.Control, log *slog.Logger, db *sql.DB) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	log.Info("starting", "version", version.Version, "commit", version.Commit, "config", cfg)
	// Valkey backs the live views, trunk status and the cluster view, not
	// management, so serving starts without it. In Sentinel mode vkconn.New
	// waits for a sentinel; until it returns, those views answer 503.
	vkc := vkconn.Config{Addr: cfg.ValkeyAddr, Sentinels: cfg.ValkeySentinels, Master: cfg.ValkeyMaster}
	vk := api.NewLazyValkey(vkc.Sentinel())
	defer vk.Close()
	go connectValkey(ctx, vkc, vk, log)

	// config.LoadControl has already checked the key's shape.
	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("HELLO_SECRET_KEY: %w", err)
	}
	st := store.New(db).WithSecretBox(box)
	go bootstrap(ctx, st, cfg.BootstrapAdminPassword, log)
	go pruneSessions(ctx, st, log)
	go seedFeatureCodes(ctx, st, log)

	// The voicemail object store and the email worker are the Phase 4
	// control-plane services (spec contract 6, S-8). minio.New does not
	// dial, so an unreachable MinIO surfaces on first use.
	objs, err := api.NewMinioObjects(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioSecure)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("MINIO_ENDPOINT: %w", err)
	}
	go ensureBuckets(ctx, objs, log)
	go runMailer(ctx, cfg, st, objs, log)

	// The node lifecycle: JOINING until PostgreSQL first answers, READY,
	// UNHEALTHY while it fails, DRAINING on SIGTERM or a drain request
	// (/readyz 503, in-flight requests finish, then exit), OFFLINE after.
	node := api.NewNode(api.NodeOptions{
		ID: cfg.NodeID, HTTPAddr: cfg.HTTPAddr, Version: version.Version,
		Postgres: db.PingContext, Valkey: vk, Revision: st.ConfigRevision,
		App: api.Handler(api.Config{
			Store:         st,
			Live:          vk,
			Trunks:        vk,
			Cluster:       vk,
			Valkey:        vk,
			Objects:       objs,
			Diagnostics:   vk,
			AuthFailLimit: cfg.AuthFailLimit,
			SIPDomain:     cfg.SIPDomain,
			SessionTTL:    cfg.SessionTTL,
			Log:           log,
		}),
		Metrics:         telemetry.NewMetrics("hello-control", version.Version, version.Commit),
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	err = node.Run(ctx.Done(), ln)
	log.Info("stopped", "error", err)
	return err
}

// seedFeatureCodes inserts the default DTMF codes missing from the table,
// retrying until PostgreSQL answers, without overwriting edited codes.
func seedFeatureCodes(ctx context.Context, st *store.Store, log *slog.Logger) {
	for {
		err := st.EnsureFeatureCodes(ctx, store.DefaultFeatureCodes())
		if err == nil {
			return
		}
		log.Warn("seed feature codes: retrying", "error", err, "retry_in", bootstrapRetry.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(bootstrapRetry):
		}
	}
}

// ensureBuckets creates the audio buckets when they are missing (deploy
// usually does; compose dev may not): hello-voicemail since Phase 4, plus
// hello-recordings and hello-announcements since Phase 5.
func ensureBuckets(ctx context.Context, objs *api.MinioObjects, log *slog.Logger) {
	bctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := objs.EnsureBucket(bctx); err != nil {
		log.Warn("voicemail bucket not verified", "bucket", api.VoicemailBucket, "error", err)
	}
	if err := objs.EnsureMediaBuckets(bctx); err != nil {
		log.Warn("media buckets not verified", "error", err)
	}
}

// runMailer starts the voicemail email queue worker. Without SMTP_HOST the
// worker is disabled: it logs and returns (spec S-8).
func runMailer(ctx context.Context, cfg config.Control, st *store.Store, objs *api.MinioObjects, log *slog.Logger) {
	if cfg.SmtpHost == "" {
		log.Info("voicemail email delivery disabled (SMTP not configured)")
		return
	}
	mailer.New(mailer.Config{
		From:    cfg.SmtpFrom,
		Queue:   queue{st},
		Objects: objs,
		Sender:  mailer.NewSMTPSender(cfg.SmtpHost, cfg.SmtpPort, cfg.SmtpUser, cfg.SmtpPass),
		Log:     log,
	}).Run(ctx)
}

// queue adapts the store's email jobs to the mailer's queue.
type queue struct{ st *store.Store }

func (q queue) PendingEmails(ctx context.Context, limit int) ([]mailer.Job, error) {
	js, err := q.st.PendingEmails(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]mailer.Job, len(js))
	for i, j := range js {
		out[i] = mailer.Job{
			MessageID: j.MessageID, To: j.To, Caller: j.Caller,
			DurationMs: j.DurationMs, CreatedAt: j.CreatedAt, Object: j.Object,
		}
	}
	return out, nil
}

func (q queue) MarkEmail(ctx context.Context, id int64, status string) error {
	return q.st.MarkEmail(ctx, id, status)
}

// bootstrap creates the first admin, retrying until the database is
// reachable and migrated, or ctx ends.
func bootstrap(ctx context.Context, st *store.Store, password string, log *slog.Logger) {
	if password == "" {
		return
	}
	for {
		err := auth.Bootstrap(ctx, st, password, log)
		if err == nil {
			return
		}
		log.Warn("bootstrap admin user: retrying", "error", err, "retry_in", bootstrapRetry.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(bootstrapRetry):
		}
	}
}

// pruneSessions deletes expired sessions now and then hourly until ctx ends.
func pruneSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		if n, err := st.PruneSessions(ctx); err != nil {
			log.Warn("prune expired sessions", "error", err)
		} else if n > 0 {
			log.Info("pruned expired sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// connectValkey creates the Valkey client and installs it in vk. A single
// instance returns at once (redialling per command if it is down); Sentinel
// waits until a sentinel answers or ctx ends.
func connectValkey(ctx context.Context, c vkconn.Config, vk *api.LazyValkey, log *slog.Logger) {
	cl, err := vkconn.New(ctx, c, log)
	if cl == nil {
		if ctx.Err() == nil {
			log.Error("valkey client could not be created; live and cluster views stay unavailable", "error", err)
		}
		return
	}
	if err != nil {
		log.Warn("valkey unreachable at startup; live and cluster views unavailable until it is", "error", err)
	}
	vk.Set(cl)
}
