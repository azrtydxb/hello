package crm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNormalizeNumber(t *testing.T) {
	for in, want := range map[string]string{
		"+32 11 22 33 44": "+3211223344", "0032 11 22 33 44": "+3211223344", "(0)11/22.33.44": "011223344",
		"+1 (415) 555-0100": "+14155550100",
	} {
		if got, ok := NormalizeNumber(in); !ok || got != want {
			t.Errorf("NormalizeNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "201", "12345", "anonymous", "sip:bob", "+32abc12345", "12+3456789"} {
		if got, ok := NormalizeNumber(in); ok {
			t.Errorf("NormalizeNumber(%q) = %q, want it refused", in, got)
		}
	}
}

func TestDisplayNameIsSafeForSIP(t *testing.T) {
	got := DisplayName("  Ann \"The\" <Boss>\r\nInj\\ected\x00  ")
	if got != "Ann The BossInjected" {
		t.Fatalf("DisplayName = %q", got)
	}
	if n := len([]rune(DisplayName(string(make([]rune, 200))))); n != 0 {
		t.Fatalf("control runes survived (%d)", n)
	}
	long := ""
	for range 100 {
		long += "x"
	}
	if n := len(DisplayName(long)); n != 64 {
		t.Fatalf("length = %d, want 64", n)
	}
}

func TestLookupCachesHitsAndMisses(t *testing.T) {
	r := newRig(t)
	r.f.contact = &Contact{ID: "c1", Name: "Ann Smith"}
	ctx := context.Background()
	if c := r.look.Find(ctx, "+32 11 22 33 44", 0); c == nil || c.ID != "c1" {
		t.Fatalf("find = %+v", c)
	}
	r.look.Find(ctx, "0032112233 44", 0) // the same number written differently
	if n := r.f.lookups.Load(); n != 1 {
		t.Fatalf("CRM queried %d times for one number, want 1", n)
	}
	r.now = r.now.Add(PositiveTTL + time.Second)
	r.look.Find(ctx, "+3211223344", 0)
	if n := r.f.lookups.Load(); n != 2 {
		t.Fatalf("after the TTL the CRM was queried %d times, want 2", n)
	}

	// A miss is cached for a minute only.
	r.f.contact = nil
	r.look.Find(ctx, "+3299887766", 0)
	r.look.Find(ctx, "+3299887766", 0)
	if n := r.f.lookups.Load(); n != 3 {
		t.Fatalf("a miss was queried %d times", n-2)
	}
	r.now = r.now.Add(NegativeTTL + time.Second)
	r.look.Find(ctx, "+3299887766", 0)
	if n := r.f.lookups.Load(); n != 4 {
		t.Fatalf("a miss was not retried after its TTL (%d)", n)
	}
	// Extensions never leave Hello.
	r.look.Find(ctx, "201", 0)
	if n := r.f.lookups.Load(); n != 4 {
		t.Fatal("an extension number was looked up")
	}
}

func TestSlowCRMNeverDelaysBeyondTheBudgetAndFillsTheCache(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	r.f.lookupFn = func(ctx context.Context) (*Contact, error) {
		select {
		case <-release:
			return &Contact{ID: "late", Name: "Late Larry"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	start := time.Now()
	name, ok := r.look.Name(context.Background(), "+3211223344", 50*time.Millisecond)
	if ok || name != "" {
		t.Fatalf("name = %q, %v from a stalled CRM", name, ok)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("the lookup held the caller for %v", d)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if name, ok := r.look.Name(context.Background(), "+3211223344", 0); ok {
			if name != "Late Larry" {
				t.Fatalf("name = %q", name)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the late answer never reached the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCRMTimeoutIsBoundedAndFailureIsNotCachedAsNoMatch(t *testing.T) {
	r := newRig(t)
	r.look.Timeout = 80 * time.Millisecond
	r.f.lookupFn = func(ctx context.Context) (*Contact, error) { <-ctx.Done(); return nil, ctx.Err() }
	start := time.Now()
	if c := r.look.Find(context.Background(), "+3211223344", 0); c != nil {
		t.Fatalf("find = %+v", c)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a hung CRM held the lookup for %v", d)
	}
	for { // let the abandoned query finish before the CRM "comes back"
		r.look.mu.Lock()
		n := len(r.look.inflight)
		r.look.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.f.lookupFn = func(context.Context) (*Contact, error) { return &Contact{ID: "x", Name: "Back"}, nil }
	if c := r.look.Find(context.Background(), "+3211223344", 0); c == nil {
		t.Fatal("a failed lookup was cached as no match")
	}
}

func TestBreakerStopsHammeringAFailingCRM(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	calls := 0
	r.f.lookupFn = func(context.Context) (*Contact, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, errors.New("boom")
	}
	for i := 0; i < 8; i++ {
		r.look.Find(context.Background(), "+32112233"+string(rune('0'+i))+"0", 0)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != breakerFails {
		t.Fatalf("the failing CRM was queried %d times, want %d before the breaker opens", n, breakerFails)
	}
	r.now = r.now.Add(breakerPause + time.Second)
	r.look.Find(context.Background(), "+3211223399", 0)
	mu.Lock()
	defer mu.Unlock()
	if calls != breakerFails+1 {
		t.Fatalf("the breaker did not close after its pause (%d calls)", calls)
	}
}

func TestLookupSkipsConnectorsThatNeedReauthOrAreDisabled(t *testing.T) {
	r := newRig(t)
	r.f.contact = &Contact{ID: "c", Name: "N"}
	cr, _, _ := r.repo.OrgConnection(context.Background(), r.conn.ID)
	_ = r.repo.SetConnectionStatus(context.Background(), cr.ID, StatusNeedsReauth, "x")
	if c := r.look.Find(context.Background(), "+3211223344", 0); c != nil {
		t.Fatalf("a connection that needs re-auth answered a lookup: %+v", c)
	}
}
