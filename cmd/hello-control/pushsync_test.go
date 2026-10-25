package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type fakeSync struct {
	stored map[int64]string
	hashes map[int64]string
	pushed []int64
}

func (f *fakeSync) AutoSyncPhones(context.Context) (map[int64]string, error) { return f.stored, nil }
func (f *fakeSync) PhoneSyncHash(_ context.Context, id int64) (string, error) {
	return f.hashes[id], nil
}
func (f *fakeSync) AutoResync(_ context.Context, id int64, old, hash string) (bool, error) {
	f.stored[id] = hash
	if old == "" {
		return false, nil
	}
	f.pushed = append(f.pushed, id)
	return true, nil
}

// TestAutoSyncDebounce fails if a phone's first hash causes a push, if a
// change pushes on first sight instead of after it held for a second scan,
// if a change that flips back before the second scan pushes, or if an
// unchanged phone is pushed.
func TestAutoSyncDebounce(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fakeSync{stored: map[int64]string{1: "", 2: "a"}, hashes: map[int64]string{1: "x", 2: "a"}}
	seen := map[int64]string{}
	ctx := context.Background()

	autoSyncScan(ctx, f, seen, log)
	if len(f.pushed) != 0 || f.stored[1] != "x" {
		t.Fatalf("first sight: pushed %v, stored %q", f.pushed, f.stored[1])
	}
	f.hashes[2] = "b"
	autoSyncScan(ctx, f, seen, log)
	if len(f.pushed) != 0 {
		t.Fatalf("change pushed before the debounce: %v", f.pushed)
	}
	f.hashes[2] = "c" // still moving: restarts the debounce
	autoSyncScan(ctx, f, seen, log)
	if len(f.pushed) != 0 {
		t.Fatalf("moving change pushed: %v", f.pushed)
	}
	autoSyncScan(ctx, f, seen, log)
	if len(f.pushed) != 1 || f.pushed[0] != 2 || f.stored[2] != "c" {
		t.Fatalf("held change: pushed %v, stored %q", f.pushed, f.stored[2])
	}
	autoSyncScan(ctx, f, seen, log)
	if len(f.pushed) != 1 {
		t.Fatalf("unchanged phone pushed again: %v", f.pushed)
	}
}
