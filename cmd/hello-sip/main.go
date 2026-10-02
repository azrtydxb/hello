// Command hello-sip is Hello's real-time SIP node. Phase 0 runs only its
// ops endpoints and Valkey readiness; the SIP listener arrives in Phase 1.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/ops"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
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

	log.Info("starting", "version", version.Version, "commit", version.Commit,
		"sip_bind", cfg.SIPBindAddr, "sip_advertised", cfg.SIPAdvertisedAddr, "valkey", cfg.ValkeyAddr)
	srv := &ops.Server{
		Checks:          map[string]ops.Check{"valkey": valkeyReady},
		Metrics:         telemetry.NewMetrics("hello-sip", version.Version, version.Commit),
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	err = srv.Serve(ctx, ln)
	log.Info("stopped", "error", err)
	return err
}
