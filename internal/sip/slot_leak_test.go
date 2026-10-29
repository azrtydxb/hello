package sip

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
)

// racingTrunkState releases the call's slots just before the refresh lands,
// the order a hangup racing the heartbeat produces.
type racingTrunkState struct {
	*fakeTrunkState
	c *call
}

func (r *racingTrunkState) RefreshTrunkCall(ctx context.Context, id int64, call string, max int, ttl time.Duration) (livestate.SlotRefresh, error) {
	r.c.releaseSlots()
	return r.fakeTrunkState.RefreshTrunkCall(ctx, id, call, max, ttl)
}

// TestRefreshDoesNotResurrectReleasedSlot fails if a heartbeat refresh that
// races the call's release leaves the slot counting against the trunk.
func TestRefreshDoesNotResurrectReleasedSlot(t *testing.T) {
	st := &racingTrunkState{fakeTrunkState: newFakeTrunkState()}
	pbx := startPBX(t, callerDevices(), trunkCfg(st))
	c := &call{s: pbx.srv, id: "c1"}
	st.c = c
	ok, why := c.acquireSlot(&routingTrunk, false, "c1")
	if !ok {
		t.Fatalf("acquire: %s", why)
	}
	if st.activeCalls(9) != 1 {
		t.Fatalf("slots = %d, want 1", st.activeCalls(9))
	}
	c.refreshSlots()
	if n := st.activeCalls(9); n != 0 {
		t.Fatalf("slot counts %d after the call released it, want 0", n)
	}
}

// TestEndStopsHeartbeat fails if ending a call leaves its heartbeat (and the
// voicemail recorder context tied to it) running.
func TestEndStopsHeartbeat(t *testing.T) {
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()))
	c := &call{s: pbx.srv, id: "c2", stopHB: make(chan struct{}), haStop: make(chan struct{})}
	pbx.srv.m.ActiveCalls.Inc()
	ctx, cancel := c.vmCtx()
	defer cancel()
	c.end(200, "caller", "", ResultAnswered)
	select {
	case <-c.stopHB:
	default:
		t.Fatal("heartbeat not stopped by end")
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("voicemail context outlived the call")
	}
}

var routingTrunk = routing.Trunk{ID: 9, Name: "ht813", Enabled: true, MaxCalls: 1}
