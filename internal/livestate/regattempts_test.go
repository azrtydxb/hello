package livestate

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRegisterAttemptsCappedNewestFirst fails if the history is not newest
// first, grows past its cap, or is left without a TTL.
func TestRegisterAttemptsCappedNewestFirst(t *testing.T) {
	s, ctx := store(t), context.Background()
	for i := range RegisterAttemptsCap + 5 {
		a := RegisterAttempt{At: time.Unix(int64(i), 0).UTC(), Device: "desk", Source: "10.0.0.9:5060", IP: "10.0.0.9",
			Node: "sip-1", Code: 401, Reason: fmt.Sprint("try ", i)}
		if err := s.RecordRegisterAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RegisterAttempts(ctx, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != RegisterAttemptsCap {
		t.Fatalf("kept %d attempts, want the cap %d", len(got), RegisterAttemptsCap)
	}
	if got[0].Reason != fmt.Sprint("try ", RegisterAttemptsCap+4) || got[len(got)-1].Reason != "try 5" {
		t.Fatalf("order = %q … %q, want newest first and the oldest 5 dropped", got[0].Reason, got[len(got)-1].Reason)
	}
	ttl, err := s.c.Do(ctx, s.c.B().Pttl().Key(regAttemptsPrefix+"desk").Build()).AsInt64()
	if err != nil || ttl <= 0 || ttl > RegisterAttemptsTTL.Milliseconds() {
		t.Fatalf("ttl = %d, %v; want within %s", ttl, err, RegisterAttemptsTTL)
	}
	if none, err := s.RegisterAttempts(ctx, "other"); err != nil || len(none) != 0 {
		t.Fatalf("unknown device = %v, %v; want empty", none, err)
	}
}

// TestAuthFailures fails if a throttle counter is not read with its window
// end, listed, or cleared.
func TestAuthFailures(t *testing.T) {
	s, ctx := store(t), context.Background()
	if err := s.c.Do(ctx, s.c.B().Set().Key(AuthFailPrefix+"192.0.2.7").Value("12").Ex(time.Minute).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	f, ok, err := s.AuthFailures(ctx, "192.0.2.7")
	if err != nil || !ok || f.Failures != 12 || f.IP != "192.0.2.7" || time.Until(f.WindowEndsAt) <= 0 {
		t.Fatalf("AuthFailures = %+v, %v, %v", f, ok, err)
	}
	if _, ok, err := s.AuthFailures(ctx, "192.0.2.8"); ok || err != nil {
		t.Fatalf("absent ip = %v, %v; want not found", ok, err)
	}
	all, err := s.AllAuthFailures(ctx)
	if err != nil || len(all) != 1 || all[0].IP != "192.0.2.7" {
		t.Fatalf("AllAuthFailures = %+v, %v", all, err)
	}
	if err := s.ClearAuthFailures(ctx, "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AuthFailures(ctx, "192.0.2.7"); ok {
		t.Fatal("counter still present after ClearAuthFailures")
	}
}
