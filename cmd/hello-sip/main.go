// Command hello-sip is Hello's real-time SIP node: a registrar and B2BUA on
// SIP/UDP, serving from an in-memory configuration snapshot and keeping its
// registrations and calls in Valkey.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/ops"
	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
	sipgosip "github.com/emiago/sipgo/sip"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hello-sip:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || args[0] != "serve" {
		return fmt.Errorf("usage: hello-sip serve")
	}
	cfg, err := config.LoadSIP(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := telemetry.NewLogger(os.Stderr, cfg.LogLevel, "hello-sip", cfg.NodeID)
	// sipgo logs through its default logger in a few places, including raw
	// datagrams it cannot parse: keep credentials out of those.
	sipgosip.SetDefaultLogger(slog.New(sip.NewRedactingHandler(log.With("component", "sipgo").Handler())))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	udp, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", cfg.SIPBindAddr)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("sip listen: %w", err)
	}
	// ForceSingleClient returns a client even when the first dial fails; it
	// redials on every command, so readiness recovers once Valkey is up.
	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{cfg.ValkeyAddr}, ForceSingleClient: true})
	if vk == nil {
		return fmt.Errorf("valkey: %w", err)
	}
	defer vk.Close()
	if err != nil {
		log.Warn("valkey unreachable at startup", "addr", cfg.ValkeyAddr, "error", err)
	}
	valkeyReady := func(ctx context.Context) error { return vk.Do(ctx, vk.B().Ping().Build()).Error() }

	metrics := telemetry.NewMetrics("hello-sip", version.Version, version.Commit)
	reloadFailures := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hello_snapshot_reload_failures_total",
		Help: "Failed configuration snapshot connects or loads; the last good snapshot stays in use.",
	})
	metrics.Registry.MustRegister(reloadFailures)
	sipMetrics := sip.NewMetrics(metrics.Registry)

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("HELLO_DATABASE_URL: malformed connection string")
	}
	poolCfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg) // connects lazily
	if err != nil {
		return fmt.Errorf("database pool: %w", err)
	}
	defer pool.Close()
	cdrs := cdr.NewWriter(pool, cdr.NewDroppedCounter(metrics.Registry), log.With("component", "cdr"))

	watcher := &snapshot.Watcher{Config: cfg.Database, Domain: cfg.SIPDomain, Log: log.With("component", "snapshot"), ReloadFailures: reloadFailures}
	srv, err := sip.New(sip.Config{
		NodeID: cfg.NodeID, Domain: cfg.SIPDomain, AdvertisedAddr: cfg.SIPAdvertisedAddr,
		NonceSecret: []byte(cfg.NonceSecret), MinExpires: cfg.RegisterMinExpires, MaxExpires: cfg.RegisterMaxExpires,
		RingTimeout: cfg.RingTimeout, AuthFailLimit: cfg.AuthFailLimit, StateTimeout: cfg.StateTimeout,
	}, sip.Deps{
		Snapshots: watcher, State: livestate.New(vk),
		Throttle: sip.ValkeyThrottle{Client: vk, Window: cfg.AuthFailWindow},
		CDRs:     cdrs, Metrics: sipMetrics, Log: log.With("component", "sip"),
	})
	if err != nil {
		return err
	}
	watcher.OnReload = srv.SnapshotChanged

	bg, stopBG := context.WithCancel(context.Background())
	defer stopBG()
	watchDone := make(chan struct{})
	go func() { watcher.Run(bg); close(watchDone) }()
	cdrDone := make(chan struct{})
	go func() { cdrs.Run(bg, 5*time.Second); close(cdrDone) }()
	sipCtx, stopSIP := context.WithCancel(context.Background())
	defer stopSIP()
	sipDone := make(chan error, 1)
	go func() { sipDone <- srv.Serve(sipCtx, udp) }()

	log.Info("starting", "version", version.Version, "commit", version.Commit, "config", cfg)
	opsSrv := &ops.Server{
		Checks: map[string]ops.Check{
			"valkey": valkeyReady,
			"snapshot": func(context.Context) error {
				if !watcher.Ready() {
					return errors.New("configuration snapshot not loaded")
				}
				return nil
			},
		},
		Metrics:         metrics,
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	// Serve drains: /readyz fails for DrainDelay while SIP keeps running, so
	// traffic moves away before the listener closes.
	err = opsSrv.Serve(ctx, ln)
	stopSIP()
	if serr := <-sipDone; serr != nil && err == nil {
		err = fmt.Errorf("sip: %w", serr)
	}
	stopBG()
	<-watchDone
	<-cdrDone
	log.Info("stopped", "error", err, "active_calls_dropped", srv.ActiveCalls())
	return err
}
