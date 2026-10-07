package main

// Phone provisioning (spec phone-auto-provisioning-service): the second
// listener phones fetch from, the daily fetch-audit pruner and the vendor
// redirect worker, each background job on one replica under a Valkey lease.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
	"github.com/azrtydxb/hello/internal/store"
)

const (
	pruneLeaseKey    = "hello:prov:lease:prune"
	redirectLeaseKey = "hello:prov:lease:redirect"
	// redirectLeaseTTL is how long the redirect lease outlives its holder;
	// it is renewed at a third of that.
	redirectLeaseTTL = 30 * time.Second
	// redirectHTTPTimeout bounds one call to a vendor's redirect API.
	redirectHTTPTimeout = 30 * time.Second
)

// provSettings is the store's view of the provisioning configuration.
func provSettings(cfg config.Control, deployment map[prov.Vendor]redirect.Credentials) store.ProvSettings {
	p := store.ProvSettings{
		SIPServer: cfg.Prov.SIPServer, Domain: cfg.SIPDomain, Expiry: cfg.Prov.RegisterExpiry,
		Resync: cfg.Prov.Resync, Timezone: cfg.Prov.Timezone, NTP: cfg.Prov.NTP, TokenGrace: cfg.Prov.TokenGrace,
		CACertFile: cfg.Prov.CACert,
		Deployment: map[prov.Vendor]bool{},
	}
	if cfg.Prov.PublicURL != nil {
		p.PublicURL = cfg.Prov.PublicURL.String()
	}
	for v := range deployment {
		p.Deployment[v] = true
	}
	return p
}

// provAPI wires the provisioning management routes.
func provAPI(cfg config.Control, settings store.ProvSettings, deployment map[prov.Vendor]redirect.Credentials, objs *api.MinioObjects, log *slog.Logger) api.ProvConfig {
	hc := &http.Client{Timeout: redirectHTTPTimeout}
	return api.ProvConfig{
		Settings: settings,
		CASHA256: caFingerprint(cfg.Prov.CACert, log),
		Firmware: objs,
		RedirectClient: func(v prov.Vendor, c redirect.Credentials, s []byte) (redirect.Client, error) {
			return redirect.New(v, c, s, hc)
		},
		Deployment: deployment,
	}
}

// caFingerprint is the hex SHA-256 of the CA certificate's DER; config has
// already checked the file is a PEM certificate.
func caFingerprint(path string, log *slog.Logger) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the operator's HELLO_PROV_CA_CERT
	if err != nil {
		log.Warn("provisioning CA certificate unreadable", "error", err)
		return ""
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return ""
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:])
}

// startProv starts the provisioning listener and its background jobs; it
// returns once the listener is bound, or with its error. Nothing starts
// while provisioning is off (no HELLO_PROV_PUBLIC_URL).
func startProv(ctx context.Context, cfg config.Control, st *store.Store, objs *api.MinioObjects, vk *api.LazyValkey,
	deployment map[prov.Vendor]redirect.Credentials, metrics *prov.Metrics, log *slog.Logger) error {
	if !cfg.Prov.Enabled() {
		log.Info("phone provisioning disabled (HELLO_PROV_PUBLIC_URL not set)")
		return nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Prov.Addr)
	if err != nil {
		return err
	}
	if cfg.Prov.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Prov.TLSCert, cfg.Prov.TLSKey)
		if err != nil {
			_ = ln.Close()
			return err
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	plog := log.With("component", "prov")
	limiter := prov.NewLazyLimiter(vk.Client, prov.Limits{
		IPPerMin: cfg.Prov.RateIPPerMin, DeniedPer10Min: cfg.Prov.RateDeniedPer10Min, PhonePerHour: cfg.Prov.RatePhonePerHour,
	}, plog)
	// The fetch audit writes off the request path; it runs (and flushes
	// what is queued) until shutdown.
	audit := prov.NewAudit(st, metrics, plog)
	go audit.Run(ctx)
	handler := prov.NewHandler(st, limiter, objs, prov.Options{
		PublicURL: cfg.Prov.PublicURL, TrustedProxies: cfg.Prov.TrustedProxies, BootCIDRs: cfg.Prov.BootCIDRs,
		CACertFile: cfg.Prov.CACert, Resync: cfg.Prov.Resync, Audit: audit, Metrics: metrics, Log: plog,
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       2 * time.Minute,
		// Firmware downloads are large; no write timeout cuts them off.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("provisioning listener stopped", "error", err)
		}
	}()
	log.Info("phone provisioning listening", "addr", cfg.Prov.Addr, "tls", cfg.Prov.TLSCert != "")
	go pruneFetches(ctx, st, vk, cfg.NodeID, cfg.Prov.AuditRetention, log)
	go runRedirectWorker(ctx, st, vk, cfg.NodeID, deployment, metrics, log)
	go publishPhoneStates(ctx, st, metrics, cfg.Prov.Resync, log)
	return nil
}

// phoneStatesEvery is how often the hello_prov_phones gauge is refreshed.
const phoneStatesEvery = time.Minute

// phoneCounter counts the phone inventory by fetch state.
type phoneCounter interface {
	PhoneFetchStates(ctx context.Context, staleBefore time.Time) (neverFetched, fetched, stale int, err error)
}

// publishPhoneStates keeps the hello_prov_phones gauge current on every
// replica: a read-only count, so no lease is needed.
func publishPhoneStates(ctx context.Context, c phoneCounter, m *prov.Metrics, resync time.Duration, log *slog.Logger) {
	t := time.NewTicker(phoneStatesEvery)
	defer t.Stop()
	for {
		if err := setPhoneStates(ctx, c, m, resync, time.Now()); err != nil && ctx.Err() == nil {
			log.Warn("count phones by fetch state", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// setPhoneStates sets the gauge once: a phone is stale when its last fetch
// is older than twice the re-check interval (spec S-17).
func setPhoneStates(ctx context.Context, c phoneCounter, m *prov.Metrics, resync time.Duration, now time.Time) error {
	never, fetched, stale, err := c.PhoneFetchStates(ctx, now.Add(-2*resync))
	if err != nil {
		return err
	}
	m.SetPhones(never, fetched, stale)
	return nil
}

// Lease is the one-replica lock of a background job.
type lease interface {
	AcquireLease(ctx context.Context, key, holder string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, key, holder string) error
}

const pruneEveryDay = 24 * time.Hour

// pruneFetches deletes fetch audit rows past the retention once a day. The
// lease (held for most of a day) makes one replica do it; while Valkey is
// down each replica prunes, which is harmless: the delete is idempotent.
func pruneFetches(ctx context.Context, st *store.Store, l lease, node string, retention time.Duration, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		held, err := l.AcquireLease(ctx, pruneLeaseKey, node, pruneEveryDay-time.Hour)
		if err != nil {
			log.Warn("fetch audit pruner: lease unavailable; pruning here", "error", err)
		}
		if held || err != nil {
			if n, err := st.PruneFetches(ctx, time.Now().Add(-retention)); err != nil {
				log.Warn("prune provisioning fetches", "error", err)
			} else if n > 0 {
				log.Info("pruned provisioning fetches", "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runRedirectWorker runs the redirect worker on the replica holding the
// redirect lease, stopping it as soon as a renewal fails, so two replicas
// never work the queue at once.
func runRedirectWorker(ctx context.Context, st *store.Store, l lease, node string, deployment map[prov.Vendor]redirect.Credentials,
	metrics *prov.Metrics, log *slog.Logger) {
	t := time.NewTicker(redirectLeaseTTL / 3)
	defer t.Stop()
	// running is the worker's stop function while this replica runs it.
	var running func()
	stop := func() {
		if running != nil {
			running()
			running = nil
		}
	}
	defer stop()
	for {
		held, err := l.AcquireLease(ctx, redirectLeaseKey, node, redirectLeaseTTL)
		switch {
		case held && running == nil:
			running = startWorker(ctx, st, deployment, metrics, log)
			log.Info("redirect worker started (lease held)")
		case !held && running != nil:
			stop()
			log.Warn("redirect worker stopped (lease lost)", "error", err)
		}
		select {
		case <-ctx.Done():
			stop()
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = l.ReleaseLease(rctx, redirectLeaseKey, node)
			rcancel()
			return
		case <-t.C:
		}
	}
}

// startWorker runs the redirect worker until the returned function stops
// it and waits for it to return.
func startWorker(ctx context.Context, st *store.Store, deployment map[prov.Vendor]redirect.Credentials, metrics *prov.Metrics, log *slog.Logger) func() {
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := redirect.NewWorker(st.Redirect(), deployment, log)
		w.Metrics = metrics
		w.Run(wctx)
	}()
	return func() {
		cancel()
		<-done
	}
}
