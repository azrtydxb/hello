// Package ops is the HTTP server every Hello service runs: liveness,
// readiness, metrics, and graceful draining on shutdown.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check reports whether one dependency is usable; a nil error means ready.
type Check func(ctx context.Context) error

// Server serves /healthz, /readyz and /metrics, plus an optional app handler
// for every other path.
type Server struct {
	Checks          map[string]Check
	App             http.Handler
	Metrics         *telemetry.Metrics
	Log             *slog.Logger
	DrainDelay      time.Duration
	ShutdownTimeout time.Duration
	CheckTimeout    time.Duration

	draining atomic.Bool
}

// Handler returns the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.Metrics.Registry, promhttp.HandlerOpts{}))
	if s.App != nil {
		mux.Handle("/", s.App)
	}
	return mux
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		s.Metrics.NodeReady.Set(0)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "draining"})
		return
	}
	timeout := s.CheckTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	failed := map[string]string{}
	for name, check := range s.Checks {
		if err := check(ctx); err != nil {
			failed[name] = err.Error()
		}
	}
	if len(failed) > 0 {
		s.Metrics.NodeReady.Set(0)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "failed": failed})
		return
	}
	s.Metrics.NodeReady.Set(1)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Serve serves on ln until ctx is cancelled, then drains: /readyz fails for
// DrainDelay while requests are still served, then in-flight requests get
// ShutdownTimeout to finish. A clean drain returns nil.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.draining.Store(true)
	s.Metrics.NodeReady.Set(0)
	s.Log.Info("draining", "drain_delay", s.DrainDelay.String())
	time.Sleep(s.DrainDelay)

	sctx, cancel := context.WithTimeout(context.Background(), s.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
