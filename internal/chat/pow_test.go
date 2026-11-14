package chat

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"
)

func sha256Sum(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// TestPow fails if a nonce is accepted after its expiry, for another site or
// address, twice, below its difficulty, or with a forged signature.
func TestPow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &clock{t: now}
	p := NewPow([]byte("k"), clk.Now)
	nonce, bits := p.Issue(7, "10.0.0.1", 14)
	if bits != 14 {
		t.Fatalf("bits = %d", bits)
	}
	sol := Solve(nonce, bits)
	if err := p.Verify(nonce, sol, 8, "10.0.0.1", 14); err == nil {
		t.Fatal("accepted for another site")
	}
	if err := p.Verify(nonce, sol, 7, "10.0.0.2", 14); err == nil {
		t.Fatal("accepted for another address")
	}
	if err := p.Verify(nonce, sol, 7, "10.0.0.1", 18); err == nil {
		t.Fatal("accepted below the site's difficulty")
	}
	if err := p.Verify(nonce, "0", 7, "10.0.0.1", 14); err == nil && leadingZeroBits(sha256Sum(nonce+":0")) < 14 {
		t.Fatal("accepted a wrong solution")
	}
	forged := nonce[:len(nonce)-2] + "AA"
	if err := p.Verify(forged, Solve(forged, 14), 7, "10.0.0.1", 14); err == nil {
		t.Fatal("accepted a forged signature")
	}
	if err := p.Verify(nonce, sol, 7, "10.0.0.1", 14); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := p.Verify(nonce, sol, 7, "10.0.0.1", 14); err == nil {
		t.Fatal("accepted twice")
	}
	n2, _ := p.Issue(7, "10.0.0.1", 14)
	clk.mu.Lock()
	clk.t = now.Add(powTTL + time.Second)
	clk.mu.Unlock()
	if err := p.Verify(n2, Solve(n2, 14), 7, "10.0.0.1", 14); err == nil {
		t.Fatal("accepted after expiry")
	}
	if _, b := p.Issue(1, "x", 99); b != MaxPowBits {
		t.Fatalf("difficulty is clamped: %d", b)
	}
	if _, b := p.Issue(1, "x", 1); b != MinPowBits {
		t.Fatalf("difficulty is clamped: %d", b)
	}
	other := NewPow([]byte("other key"), clk.Now)
	n3, _ := p.Issue(7, "10.0.0.1", 14)
	if err := other.Verify(n3, Solve(n3, 14), 7, "10.0.0.1", 14); err == nil {
		t.Fatal("a nonce signed with another key was accepted")
	}
}

// TestAdaptiveRaisesDifficulty fails if a flood does not raise the
// difficulty for 15 minutes, steady traffic does, or the metric hook is not
// called once per raise.
func TestAdaptiveRaisesDifficulty(t *testing.T) {
	raised := 0
	a := NewAdaptive(func() { raised++ })
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// A steady hour: 5 sessions a minute never raises it.
	for m := 0; m < 60; m++ {
		for i := 0; i < 5; i++ {
			a.Observe(1, base.Add(time.Duration(m)*time.Minute+time.Duration(i)*time.Second))
		}
	}
	now := base.Add(60 * time.Minute)
	if a.Extra(1, now) != 0 || raised != 0 {
		t.Fatalf("steady traffic raised the difficulty (%d)", raised)
	}
	for i := 0; i < 25; i++ {
		a.Observe(1, now.Add(time.Duration(i)*time.Second))
	}
	if a.Extra(1, now.Add(30*time.Second)) != 2 || raised != 1 {
		t.Fatalf("a flood did not raise it once: extra=%d raised=%d", a.Extra(1, now), raised)
	}
	if a.Extra(2, now.Add(30*time.Second)) != 0 {
		t.Fatal("another site was raised")
	}
	if a.Extra(1, now.Add(16*time.Minute)) != 0 {
		t.Fatal("the rise must lapse after 15 minutes")
	}
}

// TestLimiterMemory fails if the memory fallback lets a window overflow or
// never recovers.
func TestLimiterMemory(t *testing.T) {
	l := NewLimiter(nil, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if !l.Allow(ctx, "k", 3, time.Hour) {
			t.Fatalf("hit %d refused", i)
		}
	}
	if l.Allow(ctx, "k", 3, time.Hour) {
		t.Fatal("a fourth hit in the window was allowed")
	}
	if !l.Allow(ctx, "other", 3, time.Hour) {
		t.Fatal("keys share a window")
	}
	if !l.Allow(ctx, "k", 3, time.Nanosecond) {
		t.Fatal("a window that has passed still refuses")
	}
}
