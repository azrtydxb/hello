// Command hello-sip is Hello's real-time SIP node. Phase 0 runs only its
// ops endpoints and Valkey readiness; the SIP listener arrives in Phase 1.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
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
	vk := &lazyValkey{addr: cfg.ValkeyAddr}
	defer vk.Close()

	log.Info("starting", "version", version.Version, "commit", version.Commit,
		"sip_bind", cfg.SIPBindAddr, "sip_advertised", cfg.SIPAdvertisedAddr, "valkey", cfg.ValkeyAddr)
	srv := &ops.Server{
		Checks:          map[string]ops.Check{"valkey": vk.Ping},
		Metrics:         telemetry.NewMetrics("hello-sip", version.Version, version.Commit),
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	err = srv.Serve(ctx, ln)
	log.Info("stopped", "error", err)
	return err
}

// lazyValkey dials on first use and redials after a failed dial, so a node
// started before Valkey becomes ready once Valkey is reachable.
type lazyValkey struct {
	addr   string
	mu     sync.Mutex
	client valkey.Client
}

func (l *lazyValkey) Ping(ctx context.Context) error {
	l.mu.Lock()
	if l.client == nil {
		c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{l.addr}, ForceSingleClient: true})
		if err != nil {
			l.mu.Unlock()
			return err
		}
		l.client = c
	}
	c := l.client
	l.mu.Unlock()
	return c.Do(ctx, c.B().Ping().Build()).Error()
}

func (l *lazyValkey) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		l.client.Close()
	}
}
