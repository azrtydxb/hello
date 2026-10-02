package livestate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestTrunkLease fails if a second node can take an unexpired lease, if the
// holder cannot renew, or if the lease is not free after it expires.
func TestTrunkLease(t *testing.T) {
	s, ctx := store(t), context.Background()
	key := TrunkLeaseKey(7)
	if ok, err := s.AcquireLease(ctx, key, "sip-1", 400*time.Millisecond); !ok || err != nil {
		t.Fatalf("first acquire = %v, %v", ok, err)
	}
	if ok, _ := s.AcquireLease(ctx, key, "sip-2", time.Second); ok {
		t.Fatal("second node took an unexpired lease")
	}
	if ok, _ := s.AcquireLease(ctx, key, "sip-1", 400*time.Millisecond); !ok {
		t.Fatal("holder could not renew")
	}
	time.Sleep(600 * time.Millisecond)
	if ok, _ := s.AcquireLease(ctx, key, "sip-2", time.Second); !ok {
		t.Fatal("lease not free after expiry")
	}
	if err := s.ReleaseLease(ctx, key, "sip-1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AcquireLease(ctx, key, "sip-1", time.Second); ok {
		t.Fatal("a non-holder's release freed the lease")
	}
}

// TestTrunkCallSlots fails if concurrent admissions exceed max, if a
// released or expired slot keeps counting, or if re-acquiring the same call
// takes a second slot.
func TestTrunkCallSlots(t *testing.T) {
	s, ctx := store(t), context.Background()
	var (
		mu       sync.Mutex
		admitted []string
		wg       sync.WaitGroup
	)
	for i := range 20 {
		wg.Go(func() {
			id := "call-" + string(rune('a'+i))
			if ok, err := s.AcquireTrunkCall(ctx, 3, id, 5, time.Minute); err == nil && ok {
				mu.Lock()
				admitted = append(admitted, id)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(admitted) != 5 {
		t.Fatalf("admitted %d concurrent calls with max 5", len(admitted))
	}
	if ok, _ := s.AcquireTrunkCall(ctx, 3, admitted[0], 5, time.Minute); !ok {
		t.Fatal("re-acquiring a held call was refused")
	}
	if st, _ := s.TrunkStatus(ctx, 3); st.ActiveCalls != 5 {
		t.Fatalf("active calls = %d, want 5", st.ActiveCalls)
	}
	for _, c := range admitted {
		_ = s.ReleaseTrunkCall(ctx, 3, c)
	}
	if st, _ := s.TrunkStatus(ctx, 3); st.ActiveCalls != 0 {
		t.Fatalf("active calls after release = %d", st.ActiveCalls)
	}
	if ok, _ := s.AcquireTrunkCall(ctx, 9, "short", 1, 300*time.Millisecond); !ok {
		t.Fatal("empty trunk refused a call")
	}
	time.Sleep(500 * time.Millisecond)
	if ok, _ := s.AcquireTrunkCall(ctx, 9, "next", 1, time.Minute); !ok {
		t.Fatal("an expired slot still counted")
	}
}

// TestTrunkStatusRoundTrip fails if registration, health and call count do
// not read back as written, or a health field outlives its TTL.
func TestTrunkStatusRoundTrip(t *testing.T) {
	s, ctx := store(t), context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.PutTrunkRegistration(ctx, 4, TrunkRegistration{State: "registered", Node: "sip-1", LastCode: 200, UpdatedAt: now}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDestinationHealth(ctx, 4, DestinationHealth{Destination: "carrier:5060", Up: true, LastCode: 200, Latency: 3 * time.Millisecond, CheckedAt: now}, 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	st, err := s.TrunkStatus(ctx, 4)
	if err != nil || st.Registration == nil || st.Registration.State != "registered" || len(st.Destinations) != 1 || !st.Destinations[0].Up {
		t.Fatalf("status = %+v, %v", st, err)
	}
	time.Sleep(1500 * time.Millisecond) // HPEXPIRE granularity
	if st, _ := s.TrunkStatus(ctx, 4); len(st.Destinations) != 0 {
		t.Fatalf("health outlived its TTL: %+v", st.Destinations)
	}
}
