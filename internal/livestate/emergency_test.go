package livestate

import (
	"context"
	"testing"
	"time"
)

// TestEmergencyMark: a marked extension is recent until the mark expires, and
// another extension never is. It fails if the mark is not shared through the
// store or does not expire.
func TestEmergencyMark(t *testing.T) {
	s, ctx := store(t), context.Background()
	if ok, err := s.EmergencyRecent(ctx, "101"); err != nil || ok {
		t.Fatalf("unmarked = %v, %v", ok, err)
	}
	if err := s.MarkEmergency(ctx, "101", 1100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.EmergencyRecent(ctx, "101"); !ok {
		t.Fatal("marked extension is not recent")
	}
	if ok, _ := s.EmergencyRecent(ctx, "102"); ok {
		t.Fatal("another extension is recent")
	}
	time.Sleep(1300 * time.Millisecond)
	if ok, _ := s.EmergencyRecent(ctx, "101"); ok {
		t.Fatal("the mark outlived its ttl")
	}
}
