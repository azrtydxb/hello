// The takeover loop against real Valkey and real membership (CI only: the
// stores need HELLO_TEST_VALKEY_ADDR). Fails if the poller does not find an
// expired node's orphaned dialogs through the real OrphanedDialogs and
// membership paths, claim them, and re-INVITE the endpoints.
package sip

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/valkey-io/valkey-go"
)

func TestTakeoverLoopOnValkey(t *testing.T) {
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx := context.Background()
	if err := c.Do(ctx, c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	live := livestate.New(c)
	members := cluster.New(c)

	// A node that is gone: its membership record deleted (as Leave does at
	// shutdown), its tombstone left by Publish, one replicated call.
	dead := cluster.Member{ID: "sip-dead", Kind: cluster.KindSIP, State: cluster.Ready,
		SIPAddr: "10.0.0.1:5060", Version: "test", StartedAt: time.Now().UTC(), Heartbeat: time.Now().UTC()}
	if err := members.Publish(ctx, dead); err != nil {
		t.Fatal(err)
	}
	if err := members.Leave(ctx, dead.ID); err != nil {
		t.Fatal(err)
	}
	st := orphanState("ci-orphan-1", dead.ID, "sip:x@127.0.0.1:1") // the callee endpoint is gone: one-sided close
	// Shrink the owner-freshness window (2x the heartbeat) so the just
	// saved state is claimable, as it would be 4s into a real outage.
	oldHB := livestate.HAHeartbeat
	livestate.HAHeartbeat = 40 * time.Millisecond
	t.Cleanup(func() { livestate.HAHeartbeat = oldHB })
	if err := live.SaveDialogState(ctx, st, livestate.DialogTTL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * livestate.HAHeartbeat)

	mem := &fakeMembership{}
	mem.set(dead) // membership as this node reads it (the real store backs HAState)
	haReinviteTimeout = 300 * time.Millisecond
	taker := startPBX(t, nil, withNodeID("sip-live"), withHA(live, members))
	taker.srv.takeoverPass(ctx)
	eventually(t, "the orphan claimed and its takeover failed one-sidedly", func() bool {
		return taker.metric(t, "hello_zombie_calls_total", nil) == 1
	})
	// The claim went through the real Lua script and stays held.
	owner, err := live.ClaimOwner(ctx, st.CallID)
	if err != nil || owner != "sip-live" {
		t.Fatalf("ClaimOwner = %q, %v; want sip-live", owner, err)
	}
	if got, err := live.OrphanedDialogs(ctx, dead.ID); err != nil || len(got) != 0 {
		t.Fatalf("claimed dialog still orphaned: %v, %v", got, err)
	}
}
