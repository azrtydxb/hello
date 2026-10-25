package cluster

import (
	"context"
	"testing"
	"time"
)

// TestMarkSuperseded fails if a tombstone is flagged without a live node of
// the same kind and slot that started after it (hello-sip-1's replacement
// pod), or is left unflagged when there is one, or a live node, a clean
// leaver or another kind's node is touched.
func TestMarkSuperseded(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	ms := []Member{
		{ID: "hello-control-576dfb9d77-5kr5f", Kind: KindControl, State: Ready, StartedAt: t0},
		{ID: "hello-sip-1-d4f7cfdc8-2ccf7", Kind: KindSIP, State: Offline, StartedAt: t0},                // replaced
		{ID: "hello-sip-1-5c9b7d6f4-k8x2m", Kind: KindSIP, State: Ready, StartedAt: t0.Add(time.Minute)}, // its replacement
		{ID: "hello-sip-2-b97d69c-xxfcw", Kind: KindSIP, State: Offline, StartedAt: t0},                  // crashed, nobody replaced it
		{ID: "hello-sip-3-aaaaaaa-bbbbb", Kind: KindSIP, State: Offline, Left: true, StartedAt: t0},      // left cleanly
		{ID: "hello-sip-4-ccccccc-ddddd", Kind: KindSIP, State: Offline, StartedAt: t0.Add(time.Hour)},   // a live node of its slot is older
		{ID: "hello-sip-4-eeeeeee-fffff", Kind: KindSIP, State: Ready, StartedAt: t0},
		{ID: "hello-sip-5-ggggggg-hhhhh", Kind: KindSIP, State: Offline, StartedAt: t0}, // same slot name, other kind live
		{ID: "hello-sip-5-iiiiiii-jjjjj", Kind: KindControl, State: Ready, StartedAt: t0.Add(time.Minute)},
	}
	markSuperseded(ms)
	want := map[string]bool{"hello-sip-1-d4f7cfdc8-2ccf7": true}
	for _, m := range ms {
		if m.Superseded != want[m.ID] {
			t.Errorf("%s Superseded = %v, want %v", m.ID, m.Superseded, want[m.ID])
		}
	}
	gone := map[string]bool{"hello-sip-1-d4f7cfdc8-2ccf7": true, "hello-sip-3-aaaaaaa-bbbbb": true}
	for _, m := range ms {
		if m.Gone() != gone[m.ID] {
			t.Errorf("%s Gone() = %v, want %v", m.ID, m.Gone(), gone[m.ID])
		}
	}
}

// TestLeaveMarksTombstone fails if a clean Leave does not list the node as
// OFFLINE and Left (history, no alert), if a node that crashed (no Leave) is
// marked, if the replacement of a crashed node does not supersede it, or if a
// node that publishes again keeps the marker.
func TestLeaveMarksTombstone(t *testing.T) {
	s, c := store(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	pub := func(id string, started time.Time) {
		t.Helper()
		if err := s.Publish(ctx, Member{ID: id, Kind: KindSIP, State: Ready, Version: "v1", StartedAt: started, Heartbeat: now}); err != nil {
			t.Fatal(err)
		}
	}
	find := func(id string) Member {
		t.Helper()
		ms, err := s.Members(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("%s not listed in %+v", id, ms)
		return Member{}
	}

	pub("hello-sip-1-d4f7cfdc8-2ccf7", now.Add(-time.Hour))
	if err := s.Leave(ctx, "hello-sip-1-d4f7cfdc8-2ccf7"); err != nil {
		t.Fatal(err)
	}
	m := find("hello-sip-1-d4f7cfdc8-2ccf7")
	if m.State != Offline || !m.Left || !m.Gone() {
		t.Fatalf("after a clean leave = %+v, want OFFLINE and Left", m)
	}
	if ttl, _ := c.Do(ctx, c.B().Pttl().Key(tombKey("hello-sip-1-d4f7cfdc8-2ccf7")).Build()).AsInt64(); ttl <= 0 || ttl > int64(TombstoneTTL/time.Millisecond) {
		t.Fatalf("left tombstone TTL %dms, want within the tombstone period", ttl)
	}
	if err := s.Leave(ctx, "never-published"); err != nil {
		t.Fatalf("leave of an unknown node = %v", err)
	}

	// A crash: the record expires, nobody called Leave.
	pub("hello-sip-2-b97d69c-xxfcw", now.Add(-time.Hour))
	if err := c.Do(ctx, c.B().Del().Key(memberKey("hello-sip-2-b97d69c-xxfcw")).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if m := find("hello-sip-2-b97d69c-xxfcw"); m.State != Offline || m.Left || m.Superseded || m.Gone() {
		t.Fatalf("a crashed node = %+v, want OFFLINE and alerting", m)
	}
	// Its replacement pod comes up: the crash is history.
	pub("hello-sip-2-5c9b7d6f4-k8x2m", now)
	if m := find("hello-sip-2-b97d69c-xxfcw"); !m.Superseded || !m.Gone() {
		t.Fatalf("a replaced crashed node = %+v, want Superseded", m)
	}

	// The same ID publishing again (a compose restart) is live and unmarked.
	pub("hello-sip-1-d4f7cfdc8-2ccf7", now)
	if m := find("hello-sip-1-d4f7cfdc8-2ccf7"); m.State != Ready || m.Left {
		t.Fatalf("a node back under its ID = %+v", m)
	}
	if err := c.Do(ctx, c.B().Del().Key(memberKey("hello-sip-1-d4f7cfdc8-2ccf7")).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if m := find("hello-sip-1-d4f7cfdc8-2ccf7"); m.Left {
		t.Fatalf("a crash after a restart inherited the Left marker: %+v", m)
	}
}
