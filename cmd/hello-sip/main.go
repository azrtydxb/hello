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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/lifecycle"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/ops"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
	"github.com/azrtydxb/hello/internal/vkconn"
	sipgosip "github.com/emiago/sipgo/sip"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/store"
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

	// SIGTERM drains (see drainLoop); runCtx ends the process once the
	// drain is over or the SIP listener dies.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := context.WithCancel(context.Background())
	defer shutdown()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	udp, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", cfg.SIPBindAddr)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("sip listen: %w", err)
	}
	// One Valkey or a Sentinel-managed primary (vkconn). A single Valkey
	// that is down still yields a client that redials, so readiness
	// recovers once it is up.
	vk, err := vkconn.New(sigCtx, vkconn.Config{Addr: cfg.ValkeyAddr, Sentinels: cfg.ValkeySentinels, Master: cfg.ValkeyMaster}, log.With("component", "valkey"))
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
	routingInvalid := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hello_routing_config_invalid",
		Help: "1 while the current configuration revision's routing does not compile and routing is frozen on the last good table.",
	})
	routingInvalidRev := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hello_routing_config_invalid_revision",
		Help: "The configuration revision whose routing does not compile; 0 when routing is current.",
	})
	dnsFailures := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hello_dns_resolve_failures_total",
		Help: "Trunk destination DNS lookups that failed; the previous addresses are kept.",
	})
	metrics.Registry.MustRegister(reloadFailures, routingInvalid, routingInvalidRev, dnsFailures)
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

	// The voicemail store, the feature-code settings sink and the voicemail
	// audio store (Phase 4, S-8 and S-11). The writer pool is small: the SIP
	// path never waits on it, so a few connections cover voicemail writes,
	// MWI counts and feature-code changes.
	sipDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	sipDB.SetMaxOpenConns(2)
	sipDB.SetMaxIdleConns(2)
	defer func() { _ = sipDB.Close() }()
	controlStore := store.New(sipDB)
	settings := newSettings(controlStore, log.With("component", "settings"))
	voicemailObjects := &lazyObjects{
		endpoint: cfg.MinioEndpoint, access: cfg.MinioAccessKey, secret: cfg.MinioSecretKey,
		secure: cfg.MinioSecure, log: log.With("component", "media"),
	}

	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("HELLO_SECRET_KEY: %w", err)
	}
	live := livestate.New(vk)
	watcher := &snapshot.Watcher{Config: cfg.Database, Domain: cfg.SIPDomain, Log: log.With("component", "snapshot"),
		ReloadFailures: reloadFailures, Box: box, ConfigInvalid: routingInvalid, InvalidRevision: routingInvalidRev,
		DNSFailures: dnsFailures}
	srv, err := sip.New(sip.Config{
		NodeID: cfg.NodeID, Domain: cfg.SIPDomain, AdvertisedAddr: cfg.SIPAdvertisedAddr,
		NonceSecret: []byte(cfg.NonceSecret), MinExpires: cfg.RegisterMinExpires, MaxExpires: cfg.RegisterMaxExpires,
		RingTimeout: cfg.RingTimeout, AuthFailLimit: cfg.AuthFailLimit, StateTimeout: cfg.StateTimeout,
		MaxCallDuration: cfg.MaxCallDuration, TrustedProxies: cfg.TrustedProxies,
		RTPPortMin: cfg.RTPPortMin, RTPPortMax: cfg.RTPPortMax,
		MediaForceAnchor: cfg.MediaForceAnchor, MediaRecordingNotice: cfg.MediaRecordingNotice,
	}, sip.Deps{
		Snapshots: watcher, State: live, Trunks: live,
		Throttle: sip.ValkeyThrottle{Client: vk, Window: cfg.AuthFailWindow},
		CDRs:     cdrs, Metrics: sipMetrics, Log: log.With("component", "sip"),
		Presence:   sip.ValkeyPresence{Client: vk},
		Voicemails: voicemails{st: controlStore}, Objects: voicemailObjects, Settings: settings,
		Recordings: controlStore, Media: media.NewMetrics(metrics.Registry),
	})
	if err != nil {
		return err
	}
	watcher.OnReload = srv.SnapshotChanged

	members := cluster.New(vk)
	machine := lifecycle.New(lifecycle.Options{
		Member: cluster.Member{ID: cfg.NodeID, Kind: cluster.KindSIP, SIPAddr: cfg.SIPAdvertisedAddr, HTTPAddr: cfg.HTTPAddr,
			Transports: []string{"udp"}, Version: version.Version},
		Checks: map[string]lifecycle.Check{
			"valkey": valkeyReady,
			"sip": func(context.Context) error {
				if !srv.Serving() {
					return errors.New("SIP listener not running")
				}
				return nil
			},
			"snapshot": func(context.Context) error {
				if !watcher.Ready() {
					return errors.New("configuration snapshot not loaded")
				}
				return nil
			},
		},
		Publisher: members,
		Load: func() lifecycle.Load {
			l := lifecycle.Load{ActiveCalls: srv.ActiveCalls(), Registrations: srv.Registrations()}
			if s := watcher.Current(); s != nil {
				l.ConfigRevision = s.Revision
			}
			return l
		},
		Primary:   func() string { return primaryOf(vk) },
		Heartbeat: cfg.MemberHeartbeat,
		Metrics:   lifecycle.NewMetrics(metrics.Registry),
		Log:       log.With("component", "lifecycle"),
		OnChange: func(from, to cluster.State, _ string) {
			switch {
			case to == cluster.Draining:
				srv.Drain() // trunk leases go at once (contract 6)
			case from == cluster.Draining:
				srv.Undrain()
			}
		},
	})
	srv.SetLifecycle(machine)
	lcCtx, stopLifecycle := context.WithCancel(context.Background())
	lcDone := make(chan struct{})
	go func() { machine.Run(lcCtx); close(lcDone) }()
	go func() {
		<-sigCtx.Done()
		machine.Drain("SIGTERM")
	}()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		if machine.AwaitDrain(ctx, srv, cfg.DrainTimeout) {
			shutdown() // drained: exit
		}
	}()

	bg, stopBG := context.WithCancel(context.Background())
	defer stopBG()
	settingsDone := make(chan struct{})
	go func() { defer close(settingsDone); settings.Run(bg) }()
	watchDone := make(chan struct{})
	go func() { watcher.Run(bg); close(watchDone) }()
	resolveDone := make(chan struct{})
	go func() { watcher.RunResolver(bg); close(resolveDone) }()
	cdrDone := make(chan struct{})
	go func() { cdrs.Run(bg, 5*time.Second); close(cdrDone) }()
	sipCtx, stopSIP := context.WithCancel(context.Background())
	defer stopSIP()
	// A SIP listener that dies on its own takes the node down: readiness
	// fails at once and shutdown begins, rather than serving HTTP with no
	// SIP behind it.
	ctx, sipFailed := context.WithCancel(ctx)
	defer sipFailed()
	var sipDied atomic.Bool
	sipDone := make(chan error, 1)
	go func() {
		err := srv.Serve(sipCtx, udp)
		if sipCtx.Err() == nil {
			log.Error("SIP listener stopped", "error", err)
			sipDied.Store(true)
			sipFailed()
		}
		sipDone <- err
	}()

	log.Info("starting", "version", version.Version, "commit", version.Commit, "config", cfg)
	opsSrv := &ops.Server{
		Lifecycle:       machine, // /readyz follows the lifecycle state
		Metrics:         metrics,
		Log:             log,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	// Serve drains: /readyz fails for DrainDelay while SIP keeps running, so
	// traffic moves away before the listener closes.
	err = opsSrv.Serve(ctx, ln)
	stopSIP()
	if serr := <-sipDone; err == nil && (serr != nil || sipDied.Load()) {
		err = fmt.Errorf("sip: listener stopped: %w", serr)
	}
	stopLifecycle() // leaves the cluster: listed OFFLINE from the tombstone
	<-lcDone
	stopBG()
	<-watchDone
	<-resolveDone
	<-cdrDone
	log.Info("stopped", "error", err, "active_calls_dropped", srv.ActiveCalls())
	return err
}

// primaryOf is the address of the Valkey node the client sends writes to:
// the Sentinel primary (one entry in Nodes), or the single instance.
func primaryOf(vk valkey.Client) string {
	for addr := range vk.Nodes() {
		return addr
	}
	return ""
}
