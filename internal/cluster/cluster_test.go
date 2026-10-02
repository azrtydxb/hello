package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

func store(t *testing.T) (*Store, valkey.Client) {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return New(c), c
}

// TestMembersAndTombstones fails if a published member is not listed with
// its state, if a node that stopped heartbeating is not listed OFFLINE from
// its tombstone, or if OFFLINE can be published.
func TestMembersAndTombstones(t *testing.T) {
	s, c := store(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, m := range []Member{
		{ID: "hello-sip-1", Kind: KindSIP, State: Ready, ActiveCalls: 3, Version: "v1", StartedAt: now, Heartbeat: now},
		{ID: "hello-control-1", Kind: KindControl, State: Ready, Version: "v1", StartedAt: now, Heartbeat: now},
	} {
		if err := s.Publish(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Publish(ctx, Member{ID: "x", State: Offline}); err == nil {
		t.Fatal("OFFLINE was published")
	}
	ms, err := s.Members(ctx)
	if err != nil || len(ms) != 2 || ms[0].Kind != KindControl || ms[1].ActiveCalls != 3 {
		t.Fatalf("members = %+v, %v", ms, err)
	}
	// hello-sip-1 stops heartbeating: its live record expires.
	if err := c.Do(ctx, c.B().Del().Key(memberKey("hello-sip-1")).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.Members(ctx)
	if len(ms) != 2 || ms[1].ID != "hello-sip-1" || ms[1].State != Offline || ms[1].ActiveCalls != 0 {
		t.Fatalf("after expiry = %+v, want hello-sip-1 OFFLINE", ms)
	}
	if ttl, _ := c.Do(ctx, c.B().Pttl().Key(tombKey("hello-sip-1")).Build()).AsInt64(); ttl <= int64(TombstoneTTL/time.Millisecond) {
		t.Fatalf("tombstone TTL %dms, want longer than the tombstone period", ttl)
	}
}

// TestDrainRequests fails if a drain request is not seen, or survives its
// cancellation.
func TestDrainRequests(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()
	if ok, _ := s.DrainRequested(ctx, "n1"); ok {
		t.Fatal("drain requested before any request")
	}
	_ = s.RequestDrain(ctx, "n1")
	if ok, _ := s.DrainRequested(ctx, "n1"); !ok {
		t.Fatal("drain request not seen")
	}
	_ = s.CancelDrain(ctx, "n1")
	if ok, _ := s.DrainRequested(ctx, "n1"); ok {
		t.Fatal("cancelled drain still requested")
	}
}
