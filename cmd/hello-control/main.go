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

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/ops"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
	"github.com/jackc/pgx/v5/stdlib"
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
	srv := &ops.Server{
		Checks:          map[string]ops.Check{"postgres": db.PingContext},
		App:             api.Handler(),
		Metrics:         telemetry.NewMetrics("hello-control", version.Version, version.Commit),
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	err = srv.Serve(ctx, ln)
	log.Info("stopped", "error", err)
	return err
}
