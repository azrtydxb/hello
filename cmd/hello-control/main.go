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
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/ops"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/valkey-io/valkey-go"
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
	// Valkey only backs the live views, so management starts without it.
	// ForceSingleClient returns a client even when the first dial fails, and
	// that client redials on every command.
	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{cfg.ValkeyAddr}, ForceSingleClient: true})
	if vk == nil {
		_ = ln.Close()
		return fmt.Errorf("valkey: %w", err)
	}
	defer vk.Close()
	if err != nil {
		log.Warn("valkey unreachable at startup; live views unavailable until it is", "addr", cfg.ValkeyAddr, "error", err)
	}

	// config.LoadControl has already checked the key's shape.
	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("HELLO_SECRET_KEY: %w", err)
	}
	st := store.New(db).WithSecretBox(box)
	router := api.DefaultRouter()
	if router == nil {
		log.Warn("routing engine not built in: trunk and route changes and the route tester answer 503")
	}
	live := livestate.New(vk)
	go bootstrap(ctx, st, cfg.BootstrapAdminPassword, log)
	go pruneSessions(ctx, st, log)

	srv := &ops.Server{
		Checks: map[string]ops.Check{"postgres": db.PingContext},
		// Valkey only backs the live views: its loss degrades, not fails.
		Optional: map[string]ops.Check{"valkey": func(ctx context.Context) error {
			return vk.Do(ctx, vk.B().Ping().Build()).Error()
		}},
		App: api.Handler(api.Config{
			Store:      st,
			Live:       live,
			Trunks:     live,
			Router:     router,
			SIPDomain:  cfg.SIPDomain,
			SessionTTL: cfg.SessionTTL,
			Log:        log,
		}),
		Metrics:         telemetry.NewMetrics("hello-control", version.Version, version.Commit),
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	err = srv.Serve(ctx, ln)
	log.Info("stopped", "error", err)
	return err
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
