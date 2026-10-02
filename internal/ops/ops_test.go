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

// TestReadinessOptional fails if an optional dependency's failure makes
// /readyz fail, is not reported in the body and metrics, or if a required
// failure is downgraded.
func TestReadinessOptional(t *testing.T) {
	down := func(context.Context) error { return errors.New("connection refused") }
	s := newServer(nil)
	s.Optional = map[string]Check{"valkey": down}
	h := s.Handler()
	code, body := get(t, h, "/readyz")
	if code != http.StatusOK || !strings.Contains(body, `"degraded"`) || !strings.Contains(body, "valkey") {
		t.Fatalf("readyz with optional dependency down = %d %s, want 200 reporting valkey degraded", code, body)
	}
	if _, m := get(t, h, "/metrics"); !strings.Contains(m, `hello_dependency_up{dependency="valkey"} 0`) {
		t.Fatal("hello_dependency_up{valkey} not 0")
	}
	s2 := newServer(map[string]Check{"postgres": down})
	s2.Optional = map[string]Check{"valkey": func(context.Context) error { return nil }}
	if code, body := get(t, s2.Handler(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with required dependency down = %d %s, want 503", code, body)
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

// startInFlight serves s with an app handler that blocks until release is
// closed, starts one request against it, and returns the request's result
// channel plus the cancel that begins shutdown.
func startInFlight(t *testing.T, s *Server, release <-chan struct{}) (<-chan int, context.CancelFunc, <-chan error) {
	t.Helper()
	entered := make(chan struct{})
	s.App = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	result := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			result <- 0
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	<-entered
	return result, cancel, done
}

func TestShutdownWaitsForInFlight(t *testing.T) {
	s := newServer(nil)
	release := make(chan struct{})
	result, cancel, done := startInFlight(t, s, release)
	cancel()
	time.Sleep(200 * time.Millisecond) // shutdown under way, request still held
	close(release)
	if code := <-result; code != http.StatusOK {
		t.Fatalf("in-flight request = %d, want 200 completed during shutdown", code)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve = %v, want nil", err)
	}
}

func TestShutdownTimeoutExpires(t *testing.T) {
	s := newServer(nil)
	s.ShutdownTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	_, cancel, done := startInFlight(t, s, release)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Serve = %v, want deadline exceeded when a request outlives the timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not give up after ShutdownTimeout")
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
