package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/valkey-io/valkey-go"
)

// heldCalls is a Drainer whose call count the test sets, so a drain can be
// observed before it completes.
type heldCalls struct{ n atomic.Int64 }

func (h *heldCalls) ActiveCalls() int { return int(h.n.Load()) }
func (h *heldCalls) HangupAll(string) { h.n.Store(0) }

type controlNode struct {
	t       *testing.T
	members *cluster.Store
	url     string
	sig     chan struct{}
	done    chan error
	pgErr   *atomic.Pointer[error]
}

func startControlNode(t *testing.T, vc valkey.Client, id string, drainer *heldCalls) *controlNode {
	t.Helper()
	lazy := NewLazyValkey(false)
	lazy.Set(vc)
	var pgErr atomic.Pointer[error]
	down := errors.New("connection refused")
	pgErr.Store(&down)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o := NodeOptions{
		ID: id, HTTPAddr: ln.Addr().String(), Version: "v3-test", Valkey: lazy,
		Postgres: func(context.Context) error {
			if e := pgErr.Load(); e != nil {
				return *e
			}
			return nil
		},
		Revision: func(context.Context) (int64, error) { return 5, nil },
		App: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond) // an in-flight request across the drain
			w.WriteHeader(http.StatusOK)
		}),
		Metrics:    telemetry.NewMetrics("hello-control", "v3-test", "test"),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DrainDelay: 150 * time.Millisecond, ShutdownTimeout: 2 * time.Second,
		Heartbeat: 40 * time.Millisecond, CheckEvery: 10 * time.Millisecond,
	}
	if drainer != nil {
		o.Drainer = drainer
	}
	n := &controlNode{t: t, members: cluster.New(vc), url: "http://" + ln.Addr().String(), sig: make(chan struct{}),
		done: make(chan error, 1), pgErr: &pgErr}
	node := NewNode(o)
	go func() { n.done <- node.Run(n.sig, ln) }()
	return n
}

func (n *controlNode) setPostgres(err error) {
	if err == nil {
		n.pgErr.Store(nil)
		return
	}
	n.pgErr.Store(&err)
}

// readyz returns /readyz's status code and state.
func (n *controlNode) readyz() (int, string) {
	resp, err := http.Get(n.url + "/readyz")
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		State string `json:"state"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.State
}

func (n *controlNode) member(id string) cluster.Member {
	ms, err := n.members.Members(context.Background())
	if err != nil {
		n.t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	return cluster.Member{}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never held", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestControlNodeLifecycle fails if hello-control's lifecycle wiring is not
// JOINING until PostgreSQL first answers, then READY with /readyz 200, or
// if a drain request (or SIGTERM) does not make it DRAINING with /readyz
// 503 while in-flight requests finish, then exit, withdraw the request and
// be listed OFFLINE.
func TestControlNodeLifecycle(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	// DB 9: other packages flush theirs in parallel (see cluster_test.go).
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 9})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	if err := vc.Do(context.Background(), vc.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}

	t.Run("drain request", func(t *testing.T) {
		calls := &heldCalls{}
		calls.n.Store(1) // hold the drain open so DRAINING can be observed
		n := startControlNode(t, vc, "hello-control-1", calls)
		eventually(t, "JOINING published", func() bool { return n.member("hello-control-1").State == cluster.Joining })
		if code, state := n.readyz(); code != http.StatusServiceUnavailable || state != "JOINING" {
			t.Fatalf("JOINING readyz = %d %s", code, state)
		}
		n.setPostgres(nil)
		eventually(t, "READY", func() bool { code, _ := n.readyz(); return code == http.StatusOK })
		eventually(t, "READY published", func() bool { return n.member("hello-control-1").State == cluster.Ready })
		if m := n.member("hello-control-1"); m.Kind != cluster.KindControl || m.ConfigRevision != 5 || m.Version != "v3-test" {
			t.Fatalf("member = %+v", m)
		}

		if err := n.members.RequestDrain(context.Background(), "hello-control-1"); err != nil {
			t.Fatal(err)
		}
		eventually(t, "DRAINING readyz", func() bool {
			code, state := n.readyz()
			return code == http.StatusServiceUnavailable && state == "DRAINING"
		})
		eventually(t, "DRAINING published", func() bool {
			m := n.member("hello-control-1")
			return m.State == cluster.Draining && m.Reason == "drain requested"
		})
		// Still draining (the held call): readiness stays 503.
		time.Sleep(100 * time.Millisecond)
		if code, state := n.readyz(); code != http.StatusServiceUnavailable || state != "DRAINING" {
			t.Fatalf("while draining readyz = %d %s", code, state)
		}

		inflight := make(chan int, 1)
		go func() {
			resp, err := http.Get(n.url + "/slow")
			if err != nil {
				inflight <- 0
				return
			}
			_ = resp.Body.Close()
			inflight <- resp.StatusCode
		}()
		time.Sleep(50 * time.Millisecond)
		calls.n.Store(0) // drained: the node exits
		select {
		case err := <-n.done:
			if err != nil {
				t.Fatalf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the drained node did not exit")
		}
		if code := <-inflight; code != http.StatusOK {
			t.Fatalf("in-flight request = %d, want 200", code)
		}
		if req, err := n.members.DrainRequested(context.Background(), "hello-control-1"); err != nil || req {
			t.Fatalf("drain request still set after exit (%v)", err)
		}
		if m := n.member("hello-control-1"); m.State != cluster.Offline {
			t.Fatalf("after exit = %s, want OFFLINE", m.State)
		}
	})

	t.Run("SIGTERM", func(t *testing.T) {
		n := startControlNode(t, vc, "hello-control-2", nil)
		n.setPostgres(nil)
		eventually(t, "READY published", func() bool { return n.member("hello-control-2").State == cluster.Ready })
		n.setPostgres(errors.New("connection refused"))
		eventually(t, "UNHEALTHY", func() bool { return n.member("hello-control-2").State == cluster.Unhealthy })
		n.setPostgres(nil)
		eventually(t, "READY again", func() bool { return n.member("hello-control-2").State == cluster.Ready })
		close(n.sig)
		select {
		case err := <-n.done:
			if err != nil {
				t.Fatalf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the node did not exit after SIGTERM")
		}
		if m := n.member("hello-control-2"); m.State != cluster.Offline {
			t.Fatalf("after SIGTERM = %s, want OFFLINE", m.State)
		}
	})
}
