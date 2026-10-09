package voice

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestVoicePrune(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src, WithRetention(45*24*time.Hour))
	ctx := context.Background()

	// The prune runs under the advisory lock and passes the retention the
	// service was built with (spec S-23).
	ran, err := svc.Prune(ctx)
	if err != nil || !ran {
		t.Fatalf("Prune = %v, %v; want ran with no error", ran, err)
	}
	if len(src.pruned) != 1 {
		t.Fatalf("%d prunes ran, want 1", len(src.pruned))
	}
	if src.pruned[0].retention != 45*24*time.Hour {
		t.Fatalf("retention %s reached the store, want 45 days", src.pruned[0].retention)
	}

	// Another replica holds the lock: nothing runs, no error (spec S-23:
	// two replicas never prune at once).
	src.lockHeld = true
	ran, err = svc.Prune(ctx)
	if err != nil || ran {
		t.Fatalf("Prune with the lock held = %v, %v; want false, nil", ran, err)
	}
	if len(src.pruned) != 1 {
		t.Fatal("a prune ran without the lock")
	}

	// A store failure propagates.
	src.lockHeld = false
	src.pruned = nil
	src.pruneErr = errors.New("boom")
	if _, err = svc.Prune(ctx); err == nil {
		t.Fatal("a store failure must surface")
	}
}

func TestVoicePruneLoop(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	go svc.PruneLoop(ctx)
	cancel()
}
