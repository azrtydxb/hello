package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

// TestLease fails if two holders can hold one lease, if the holder cannot
// renew it, if it does not lapse after its TTL, or if a release by another
// holder frees it.
func TestLease(t *testing.T) {
	addr := testValkeyAddr()
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	l := NewLazyValkey(false)
	l.Set(vc)
	ctx := context.Background()
	key := fmt.Sprintf("hello:test:lease:%d", time.Now().UnixNano())
	ttl := 300 * time.Millisecond

	if ok, err := l.AcquireLease(ctx, key, "a", ttl); !ok || err != nil {
		t.Fatalf("a acquire = %v, %v", ok, err)
	}
	if ok, err := l.AcquireLease(ctx, key, "b", ttl); ok || err != nil {
		t.Fatalf("b acquired a held lease: %v, %v", ok, err)
	}
	if err := l.ReleaseLease(ctx, key, "b"); err != nil {
		t.Fatal(err)
	}
	// Renewing past the first TTL keeps it a's.
	for range 3 {
		time.Sleep(ttl / 2)
		if ok, err := l.AcquireLease(ctx, key, "a", ttl); !ok || err != nil {
			t.Fatalf("a renew = %v, %v", ok, err)
		}
	}
	if ok, _ := l.AcquireLease(ctx, key, "b", ttl); ok {
		t.Fatal("b took a renewed lease")
	}
	time.Sleep(ttl + 100*time.Millisecond)
	if ok, err := l.AcquireLease(ctx, key, "b", ttl); !ok || err != nil {
		t.Fatalf("b after expiry = %v, %v", ok, err)
	}
	if err := l.ReleaseLease(ctx, key, "b"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.AcquireLease(ctx, key, "a", ttl); !ok {
		t.Fatal("a could not take a released lease")
	}
	if _, err := NewLazyValkey(false).AcquireLease(ctx, key, "a", ttl); err == nil {
		t.Fatal("an unconnected client reported a lease")
	}
}
