package vkconn

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestSingleAndSentinelStartup fails if single mode does not return a usable
// client, or if Sentinel mode with no reachable sentinel does not keep
// retrying until its context ends.
func TestSingleAndSentinelStartup(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	if addr := os.Getenv("HELLO_TEST_VALKEY_ADDR"); addr != "" {
		c, err := New(context.Background(), Config{Addr: addr}, log)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Do(context.Background(), c.B().Ping().Build()).Error(); err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New(ctx, Config{Sentinels: []string{"127.0.0.1:1"}, Master: "hello"}, log)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 2*time.Second {
		t.Fatalf("sentinel startup = %v after %s, want to retry until the deadline", err, time.Since(start))
	}
}
