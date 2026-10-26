package livestate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestParkSlots: a slot is reserved once, claimed exactly once, refreshed
// and released only by its own call, and listed in slot order. It fails if
// two reservations of a slot both succeed, two claims both get the call, a
// stranger's Call-ID refreshes or releases the slot, or the list is out of
// order.
func TestParkSlots(t *testing.T) {
	s, ctx := store(t), context.Background()
	rec := func(slot, callID string) ParkedCall {
		return ParkedCall{Slot: slot, Lot: "default", Node: "sip-1", CallID: callID, ParkedBy: "101", Caller: "102",
			ParkedAt: time.Now(), ReturnAt: time.Now().Add(time.Minute), Phase: ParkParked}
	}
	if ok, err := s.ParkReserve(ctx, rec("701", "a"), time.Minute); err != nil || !ok {
		t.Fatalf("first reserve = %v, %v", ok, err)
	}
	if ok, _ := s.ParkReserve(ctx, rec("701", "b"), time.Minute); ok {
		t.Fatal("a taken slot was reserved twice")
	}
	for _, slot := range []string{"710", "702"} {
		if ok, err := s.ParkReserve(ctx, rec(slot, "x"+slot), time.Minute); err != nil || !ok {
			t.Fatalf("reserve %s = %v, %v", slot, ok, err)
		}
	}
	list, err := s.ParkList(ctx)
	if err != nil || len(list) != 3 || list[0].Slot != "701" || list[1].Slot != "702" || list[2].Slot != "710" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if ok, _ := s.ParkRefresh(ctx, "701", "stranger", time.Minute); ok {
		t.Fatal("a stranger refreshed the slot")
	}
	if ok, err := s.ParkRefresh(ctx, "701", "a", time.Minute); err != nil || !ok {
		t.Fatalf("own refresh = %v, %v", ok, err)
	}
	if err := s.ParkRelease(ctx, "701", "stranger"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ParkGet(ctx, "701"); !ok {
		t.Fatal("a stranger released the slot")
	}
	p := rec("701", "a")
	p.Phase = ParkReturning
	if ok, err := s.ParkUpdate(ctx, p, time.Minute); err != nil || !ok {
		t.Fatalf("update = %v, %v", ok, err)
	}
	if got, _, _ := s.ParkGet(ctx, "701"); got.Phase != ParkReturning {
		t.Fatalf("phase after update = %q", got.Phase)
	}
	if err := s.ParkRelease(ctx, "701", "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ParkGet(ctx, "701"); ok {
		t.Fatal("the owner's release left the slot")
	}

	// Two retrievers racing for one slot: exactly one gets the call.
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := s.ParkClaim(ctx, "702"); err == nil && ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d claims won the slot, want 1", won.Load())
	}
	if _, ok, _ := s.ParkClaim(ctx, "702"); ok {
		t.Fatal("a claimed slot was claimed again")
	}

	// A slot lapses with its record (the owner died).
	if ok, _ := s.ParkReserve(ctx, rec("703", "z"), 300*time.Millisecond); !ok {
		t.Fatal("reserve 703")
	}
	time.Sleep(500 * time.Millisecond)
	if _, ok, _ := s.ParkGet(ctx, "703"); ok {
		t.Fatal("the slot outlived its record")
	}
}

// TestParkCommands: commands are taken once, in order, only by the node they
// name, and a reply is found by its id.
func TestParkCommands(t *testing.T) {
	s, ctx := store(t), context.Background()
	if _, ok, err := s.ParkTake(ctx, "sip-2"); err != nil || ok {
		t.Fatalf("empty = %v, %v", ok, err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.ParkSend(ctx, "sip-2", ParkCommand{ID: id, Op: ParkOpRetrieve, Slot: "701"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := s.ParkTake(ctx, "sip-1"); ok {
		t.Fatal("another node took the command")
	}
	for _, want := range []string{"a", "b"} {
		c, ok, err := s.ParkTake(ctx, "sip-2")
		if err != nil || !ok || c.ID != want || c.Slot != "701" {
			t.Fatalf("take = %+v, %v, %v; want %s", c, ok, err, want)
		}
	}
	if _, ok, _ := s.ParkGetReply(ctx, "a"); ok {
		t.Fatal("reply before it was posted")
	}
	if err := s.ParkPutReply(ctx, ParkReply{ID: "a", OK: true, Answer: "v=0"}); err != nil {
		t.Fatal(err)
	}
	r, ok, err := s.ParkGetReply(ctx, "a")
	if err != nil || !ok || !r.OK || r.Answer != "v=0" {
		t.Fatalf("reply = %+v, %v, %v", r, ok, err)
	}
	if _, ok, _ := s.ParkGetReply(ctx, "b"); ok {
		t.Fatal("a reply showed for another id")
	}
}
