package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
)

type fakeWebRTCPub struct {
	mu      sync.Mutex
	put     []livestate.WebRTCNodeStatus
	ttl     time.Duration
	deleted string
}

func (f *fakeWebRTCPub) PutWebRTCStatus(_ context.Context, st livestate.WebRTCNodeStatus, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put, f.ttl = append(f.put, st), ttl
	return nil
}

func (f *fakeWebRTCPub) DeleteWebRTCStatus(_ context.Context, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = node
	return nil
}

// TestRunWebRTCStatus fails if the node does not publish its WebRTC status at
// once (node, port, candidate addresses, non-nil lists and counters) with a
// TTL longer than the period, or does not remove it when it stops.
func TestRunWebRTCStatus(t *testing.T) {
	node, err := media.StartWebRTC(0, "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	pub := &fakeWebRTCPub{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		runWebRTCStatus(ctx, pub, node, "sip-1", slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pub.mu.Lock()
		n := len(pub.put)
		pub.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing published")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	pub.mu.Lock()
	defer pub.mu.Unlock()
	st := pub.put[0]
	if st.Node != "sip-1" || st.Port != node.Mux().Port() || len(st.Hosts) != 1 || st.Hosts[0] != "127.0.0.1" ||
		st.Legs == nil || st.Failures == nil || st.Counters.ICE == nil || pub.ttl <= webrtcStatusEvery {
		t.Fatalf("published %+v ttl %v", st, pub.ttl)
	}
	if pub.deleted != "sip-1" {
		t.Fatalf("status not removed on stop (deleted %q)", pub.deleted)
	}
}
