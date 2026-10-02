package ops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/telemetry"
)

func newServer(checks map[string]Check) *Server {
	return &Server{
		Checks:          checks,
		Metrics:         telemetry.NewMetrics("test", "v0", "abc"),
		Log:             slog.New(slog.DiscardHandler),
		ShutdownTimeout: time.Second,
	}
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestReadiness(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	s := newServer(map[string]Check{"postgres": func(context.Context) error {
		if down.Load() {
			return errors.New("connection refused")
		}
		return nil
	}})
	h := s.Handler()

	if code, _ := get(t, h, "/healthz"); code != http.StatusOK {
		t.Fatalf("healthz with dependency down = %d, want 200", code)
	}
	code, body := get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "postgres") {
		t.Fatalf("readyz with dependency down = %d %s, want 503 naming postgres", code, body)
	}
	down.Store(false)
	if code, body := get(t, h, "/readyz"); code != http.StatusOK {
		t.Fatalf("readyz with dependency up = %d %s, want 200", code, body)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := newServer(nil).Handler()
	get(t, h, "/readyz") // sets hello_node_ready
	code, body := get(t, h, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("metrics = %d", code)
	}
	for _, m := range []string{"hello_node_ready 1", `hello_build_info{commit="abc",service="test",version="v0"} 1`} {
		if !strings.Contains(body, m) {
			t.Fatalf("metrics missing %q", m)
		}
	}
}

func TestGracefulShutdown(t *testing.T) {
	s := newServer(nil)
	s.DrainDelay = 300 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String() + "/readyz"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	status := func() int {
		resp, err := http.Get(url)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if c := status(); c != http.StatusOK {
		t.Fatalf("readyz before shutdown = %d, want 200", c)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	if c := status(); c != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining = %d, want 503 from a still-open listener", c)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within the timeout")
	}
}
