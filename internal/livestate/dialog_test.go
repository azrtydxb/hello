package livestate

import (
	"context"
	"testing"
	"time"
)

// TestDialogStateLifecycle fails if the claim is not atomic (two claimants,
// one winner), if the owner's fresh heartbeat blocks a claim while an aged
// one does not, if a claimed dialog is still offered to other takers, or if
// a record outlives its TTL. Runs only against a real Valkey (CI).
func TestDialogStateLifecycle(t *testing.T) {
	s, ctx := store(t), context.Background()
	// The freshness window (2x HAHeartbeat) is set per phase, so neither
	// phase depends on how fast the runner is: an hour while the record
	// must count as fresh, a millisecond once it must count as aged.
	oldHB := HAHeartbeat
	HAHeartbeat = time.Hour
	t.Cleanup(func() { HAHeartbeat = oldHB })

	st := DialogState{
		CallID: "c1", OwnerNode: "sip-1", Correlation: "corr-1", State: "talking",
		RelayPorts: [2]int{20000, 20002},
		Legs: [2]DialogLeg{
			{CallID: "legA", LocalTag: "ta", RemoteTag: "ra", LocalCSeq: 3, RemoteCSeq: 2,
				RouteSet:     []string{"<sip:kam:5070;lr>"},
				Contact:      "sip:hello-sip-1:5060;transport=udp",
				RemoteTarget: "sip:101@10.0.0.9:5060", SDP: "v=0", Endpoint: "sip:101@hello.test"},
			{CallID: "legB", LocalTag: "tb", RemoteTag: "rb", LocalCSeq: 2, RemoteCSeq: 5,
				Contact:      "sip:hello-sip-1:5060;transport=udp",
				RemoteTarget: "sip:102@10.0.0.10:5060", Endpoint: "sip:102@hello.test"},
		},
	}
	if err := s.SaveDialogState(ctx, st, 0); err != nil {
		t.Fatal(err)
	}

	// The record's expiry is the heartbeat: PTTL within the HA TTL.
	pttl, err := s.c.Do(ctx, s.c.B().Pttl().Key(dialogKey("c1")).Build()).AsInt64()
	if err != nil || pttl <= 0 || pttl > int64(DialogTTL/time.Millisecond) {
		t.Fatalf("PTTL = %d, %v; want 0 < pttl <= %d", pttl, err, int64(DialogTTL/time.Millisecond))
	}

	// A dialog of another node is invisible; the owner's is listed.
	if got, err := s.OrphanedDialogs(ctx, "sip-2"); err != nil || len(got) != 0 {
		t.Fatalf("OrphanedDialogs(other node) = %v, %v", got, err)
	}
	got, err := s.OrphanedDialogs(ctx, "sip-1")
	if err != nil || len(got) != 1 || got[0].CallID != "c1" {
		t.Fatalf("OrphanedDialogs(sip-1) = %v, %v; want [c1]", got, err)
	}

	// Fresh heartbeat: a claim is refused while the owner looks alive.
	if ok, _, err := s.ClaimDialog(ctx, "c1", "sip-2", ""); ok || err != nil {
		t.Fatalf("fresh claim = %v, %v; want refused", ok, err)
	}

	// After the freshness window the claim succeeds exactly once.
	HAHeartbeat = time.Millisecond
	time.Sleep(2*HAHeartbeat + 20*time.Millisecond)
	ok, state, err := s.ClaimDialog(ctx, "c1", "sip-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("aged claim refused")
	}
	if state.CallID != "c1" || state.OwnerNode != "sip-1" || state.Legs[0].LocalCSeq != 3 ||
		state.Legs[1].RemoteCSeq != 5 || state.RelayPorts[1] != 20002 {
		t.Fatalf("claimed state incomplete: %+v", state)
	}

	// A claimed dialog is not offered to other survivors, and the second
	// claimant loses the race.
	if got, _ := s.OrphanedDialogs(ctx, "sip-1"); len(got) != 0 {
		t.Fatalf("claimed dialog still orphaned: %v", got)
	}
	if ok, _, _ := s.ClaimDialog(ctx, "c1", "sip-3", ""); ok {
		t.Fatal("second claimant won")
	}

	// The owner sees the claim (the yield signal) and who holds it.
	owner, err := s.ClaimOwner(ctx, "c1")
	if err != nil || owner != "sip-2" {
		t.Fatalf("ClaimOwner = %q, %v; want sip-2", owner, err)
	}

	// Release puts the dialog back in the orphan list (claim expiry does
	// too, but the test cannot wait 30s).
	if err := s.ReleaseDialogClaim(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.OrphanedDialogs(ctx, "sip-1"); len(got) != 1 {
		t.Fatalf("dialog not orphaned again after release: %v", got)
	}
	if owner, _ := s.ClaimOwner(ctx, "c1"); owner != "" {
		t.Fatalf("claim survived release: %q", owner)
	}

	// Expiry: a short-TTL record vanishes, leaving nothing to claim.
	if err := s.SaveDialogState(ctx, st, 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if ok, _, _ := s.ClaimDialog(ctx, "c1", "sip-2", ""); ok {
		t.Fatal("claimed a dialog whose state expired")
	}
	if got, _ := s.OrphanedDialogs(ctx, "sip-1"); len(got) != 0 {
		t.Fatalf("expired dialog still listed: %v", got)
	}
}

// TestDialogClaimStaleIncarnation fails if the record of a dead process of
// a node restarted in place under the same ID cannot be claimed at once
// (its last write is fresh: the freshness window must not apply when the
// owner incarnation is known dead), if a claim naming another incarnation
// than the record's succeeds (the live process's own calls), if a record
// rewritten between the read and the claim is claimed (compare-and-set),
// or if the callee leg's Call-ID does not find the record. Runs only
// against a real Valkey (CI).
func TestDialogClaimStaleIncarnation(t *testing.T) {
	s, ctx := store(t), context.Background()
	st := DialogState{
		CallID: "c2", OwnerNode: "sip-1", OwnerIncarnation: "old", Correlation: "corr-2", State: "talking",
		StartedAt: time.Now().Add(-time.Minute).UTC(),
		Legs: [2]DialogLeg{
			{CallID: "c2", LocalTag: "ta", RemoteTag: "ra", RemoteTarget: "sip:101@10.0.0.9"},
			{CallID: "c2-b", LocalTag: "tb", RemoteTag: "rb", RemoteTarget: "sip:102@10.0.0.10"},
		},
	}
	if err := s.SaveDialogState(ctx, st, 0); err != nil {
		t.Fatal(err)
	}
	// Either leg's Call-ID finds the record.
	for _, id := range []string{"c2", "c2-b"} {
		got, ok, err := s.DialogByLeg(ctx, id)
		if err != nil || !ok || got.CallID != "c2" || got.OwnerIncarnation != "old" || got.StartedAt.IsZero() {
			t.Fatalf("DialogByLeg(%s) = %+v, %v, %v", id, got, ok, err)
		}
	}
	if _, ok, err := s.DialogByLeg(ctx, "nope"); ok || err != nil {
		t.Fatalf("DialogByLeg(unknown) = %v, %v", ok, err)
	}
	// Fresh, and no incarnation named: refused as before.
	if ok, _, err := s.ClaimDialog(ctx, "c2", "sip-1", ""); ok || err != nil {
		t.Fatalf("fresh claim = %v, %v; want refused", ok, err)
	}
	// Another incarnation than the record's: refused (a live process).
	if ok, _, err := s.ClaimDialog(ctx, "c2", "sip-1", "other"); ok || err != nil {
		t.Fatalf("claim naming the wrong incarnation = %v, %v; want refused", ok, err)
	}
	// The dead incarnation named: claimed at once, fresh or not, counted
	// apart from the node's own (OFFLINE) takeovers.
	ok, got, err := s.ClaimDialog(ctx, "c2", "sip-1", "old")
	if err != nil || !ok || got.CallID != "c2" {
		t.Fatalf("stale-incarnation claim = %v, %+v, %v; want claimed", ok, got, err)
	}
	if n, _ := s.TakenOver(ctx, "sip-1"); n != 0 {
		t.Fatalf("TakenOver(sip-1) = %d: a restart's claims must not offset the node's OFFLINE reap", n)
	}
	if owner, _ := s.ClaimOwner(ctx, "c2"); owner != "sip-1" {
		t.Fatalf("ClaimOwner = %q", owner)
	}

	// Compare-and-set: the script refuses a record that changed since it
	// was read.
	if err := s.ReleaseDialogClaim(ctx, "c2"); err != nil {
		t.Fatal(err)
	}
	n, err := claimOrphan.Exec(ctx, s.c, []string{dialogClaimKey("c2"), dialogKey("c2"), dialogTakenKey("x")},
		[]string{"sip-2", "30000", "60000", `{"callId":"c2","ownerNode":"sip-9"}`}).AsInt64()
	if err != nil || n != 0 {
		t.Fatalf("claim on a rewritten record = %d, %v; want refused", n, err)
	}
}
