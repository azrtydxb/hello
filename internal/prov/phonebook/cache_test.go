package phonebook

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/azrtydxb/hello/internal/vkconn"
)

// TestCacheValkeyDown fails if a cache whose Valkey is unreachable, or not
// yet connected, answers a hit, blocks a request past its bound or reports
// an error: a render must fall back to PostgreSQL (spec P-11). It needs no
// Valkey (the client points at a closed port).
func TestCacheValkeyDown(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	c, _ := vkconn.New(context.Background(), vkconn.Config{Addr: "127.0.0.1:1"}, log)
	if c == nil {
		t.Fatal("no client")
	}
	defer c.Close()
	cache := NewCache(func() valkey.Client { return c }, log)
	start := time.Now()
	cache.Set(t.Context(), RenderKeyPrefix+"yealink:x", []byte("<x/>"), time.Minute)
	if b, ok := cache.Get(t.Context(), RenderKeyPrefix+"yealink:x"); ok {
		t.Fatalf("hit with Valkey down: %q", b)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Valkey down held the request for %s", d)
	}

	// Still connecting: no client yet.
	lazy := NewCache(func() valkey.Client { return nil }, nil)
	lazy.Set(t.Context(), "k", []byte("v"), time.Minute)
	if _, ok := lazy.Get(t.Context(), "k"); ok {
		t.Fatal("hit with no client")
	}
}

// TestPhonebookCacheHA (spec P-11) fails if a render written through one
// cache instance is not visible through another on the next request, if a
// Valkey flush yields anything but a miss, or if an entry outlives its ttl.
// It runs against the CI Valkey.
func TestPhonebookCacheHA(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 13}) // prov/phonebook's own DB: other packages FLUSHDB theirs in parallel (TestValkeyDBsPerPackage)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := t.Context()
	if err := c.Do(ctx, c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	// Two replicas, one Valkey; the bound is wider than production's so a
	// loaded CI runner does not turn a hit into a miss.
	a, b := NewCache(func() valkey.Client { return c }, nil), NewCache(func() valkey.Client { return c }, nil)
	a.timeout, b.timeout = 5*time.Second, 5*time.Second

	key := RenderKeyPrefix + "yealink:abc"
	if _, ok := b.Get(ctx, key); ok {
		t.Fatal("hit before any write")
	}
	body := []byte("<YealinkIPPhoneDirectory/>\n\x00\xff binary-safe")
	a.Set(ctx, key, body, time.Hour)
	got, ok := b.Get(ctx, key)
	if !ok || string(got) != string(body) {
		t.Fatalf("a write through one instance is not visible through another: %q, %v", got, ok)
	}
	if ttl, err := c.Do(ctx, c.B().Ttl().Key(key).Build()).AsInt64(); err != nil || ttl < 3500 || ttl > 3600 {
		t.Fatalf("ttl = %d, %v; want about an hour", ttl, err)
	}
	// A new version is a new key, so the old render is never served for it.
	if _, ok := b.Get(ctx, RenderKeyPrefix+"yealink:def"); ok {
		t.Fatal("another version's key hit")
	}

	// A flush loses the render and nothing else: the next read is a miss
	// (the handler renders again from PostgreSQL), never a stale body.
	if err := c.Do(ctx, c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Get(ctx, key); ok {
		t.Fatal("hit after a Valkey flush")
	}
	a.Set(ctx, key, body, 1*time.Second)
	time.Sleep(1200 * time.Millisecond)
	if _, ok := b.Get(ctx, key); ok {
		t.Fatal("an entry outlived its ttl")
	}
}
