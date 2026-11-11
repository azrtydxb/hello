package livestate

import (
	"context"
	"testing"
	"time"
)

// TestDoorHookState fails if a queued trigger is not taken exactly once in
// order, if a trigger older than its TTL is rung, if the rate window does not
// count up and expire, or if a duplicate claim inside the window succeeds.
func TestDoorHookState(t *testing.T) {
	s, ctx := store(t), context.Background()
	if _, ok, err := s.TakeDoorTrigger(ctx); err != nil || ok {
		t.Fatalf("empty queue = %v, %v", ok, err)
	}
	now := time.Now()
	for _, tr := range []DoorTrigger{
		{ID: "stale", Webhook: 1, At: now.Add(-2 * DoorTriggerTTL)},
		{ID: "a", Webhook: 1, Name: "Gate", Action: "ring_door", At: now},
		{ID: "b", Webhook: 2, Action: "chime_page", PagingCode: "*60", At: now},
	} {
		if err := s.EnqueueDoorTrigger(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b"} {
		got, ok, err := s.TakeDoorTrigger(ctx)
		if err != nil || !ok || got.ID != want {
			t.Fatalf("take = %+v, %v, %v; want %s (the stale one is dropped)", got, ok, err, want)
		}
	}
	if _, ok, _ := s.TakeDoorTrigger(ctx); ok {
		t.Fatal("a trigger was taken twice")
	}

	for i := int64(1); i <= 3; i++ {
		n, left, err := s.DoorHookRate(ctx, 7, 400*time.Millisecond)
		if err != nil || n != i || left <= 0 || left > 400*time.Millisecond {
			t.Fatalf("count %d = %d, %v, %v", i, n, left, err)
		}
	}
	if n, _, _ := s.DoorHookRate(ctx, 8, time.Minute); n != 1 {
		t.Errorf("another webhook's count = %d, want its own 1", n)
	}
	time.Sleep(500 * time.Millisecond)
	if n, _, _ := s.DoorHookRate(ctx, 7, 400*time.Millisecond); n != 1 {
		t.Errorf("count after the window = %d, want 1", n)
	}

	if ok, err := s.DoorHookClaim(ctx, 9, 300*time.Millisecond); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if ok, _ := s.DoorHookClaim(ctx, 9, 300*time.Millisecond); ok {
		t.Error("a duplicate claim inside the window succeeded")
	}
	if ok, _ := s.DoorHookClaim(ctx, 10, 300*time.Millisecond); !ok {
		t.Error("another webhook's claim was refused")
	}
	time.Sleep(400 * time.Millisecond)
	if ok, _ := s.DoorHookClaim(ctx, 9, 300*time.Millisecond); !ok {
		t.Error("a claim after the window was refused")
	}
}
