package cluster

import (
	"context"
	"testing"
	"time"
)

// TestRolloutToken fails if the restart token is not exclusive, is not
// renewable by its holder, can be released by another node, is shared
// between node kinds, or does not expire (a holder that died must not block
// its peers for ever).
func TestRolloutToken(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()
	if h, err := s.AcquireRollout(ctx, KindSIP, "hello-sip-1", time.Minute); err != nil || h != "hello-sip-1" {
		t.Fatalf("first acquire = %q, %v", h, err)
	}
	if h, err := s.AcquireRollout(ctx, KindSIP, "hello-sip-2", time.Minute); err != nil || h != "hello-sip-1" {
		t.Fatalf("second node acquire = %q, %v, want the holder hello-sip-1", h, err)
	}
	if h, err := s.AcquireRollout(ctx, KindSIP, "hello-sip-1", time.Minute); err != nil || h != "hello-sip-1" {
		t.Fatalf("renewal = %q, %v", h, err)
	}
	if err := s.ReleaseRollout(ctx, KindSIP, "hello-sip-2"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.AcquireRollout(ctx, KindSIP, "hello-sip-2", time.Minute); h != "hello-sip-1" {
		t.Fatalf("a non-holder released the token: holder %q", h)
	}
	if h, _ := s.AcquireRollout(ctx, KindControl, "hello-control-1", time.Minute); h != "hello-control-1" {
		t.Fatalf("kinds share a token: holder %q", h)
	}
	if err := s.ReleaseRollout(ctx, KindSIP, "hello-sip-1"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.AcquireRollout(ctx, KindSIP, "hello-sip-2", 50*time.Millisecond); h != "hello-sip-2" {
		t.Fatalf("after release holder = %q, want hello-sip-2", h)
	}
	time.Sleep(120 * time.Millisecond)
	if h, _ := s.AcquireRollout(ctx, KindSIP, "hello-sip-1", time.Minute); h != "hello-sip-1" {
		t.Fatalf("an expired token still held by %q", h)
	}
}
