package oauth

import (
	"context"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

// TestThrottleValkeyTimeoutKeepsCount fails if a Valkey call that times out
// mid-window falls back to an empty memory window, letting the hit after the
// limit through (the "registration 11 = 201" flake under -race).
func TestThrottleValkeyTimeoutKeepsCount(t *testing.T) {
	l := NewLimiter(func() valkey.Client { return valkey.Client(stubClient{}) }, nil).(*valkeyLimiter)
	l.client = func() valkey.Client { return stubClient{} }
	var hits, calls int64
	l.run = func(_ context.Context, _ valkey.Client, _ string, limit int, _ time.Duration, _ string) (int64, error) {
		if calls++; calls == 6 { // one call times out, Valkey never counts it
			return 0, context.DeadlineExceeded
		}
		if hits >= int64(limit) {
			return 0, nil
		}
		hits++
		return 1, nil
	}
	for i := 1; i <= 11; i++ {
		ok, err := l.Allow(context.Background(), "k", 10, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if want := i <= 10; ok != want {
			t.Fatalf("hit %d allowed = %v, want %v", i, ok, want)
		}
	}
}

// stubClient is a non-nil client; the fake run never touches it.
type stubClient struct{ valkey.Client }
