package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

// stores returns the Store implementations to test: the in-process one
// always, Valkey when HELLO_TEST_VALKEY_ADDR is set (CI). Every test below
// runs against both, so they must behave the same.
func stores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMemStore()}
	if addr := os.Getenv("HELLO_TEST_VALKEY_ADDR"); addr != "" {
		c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 1}) // queue's DB (TestValkeyDBsPerPackage)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
			t.Fatal(err)
		}
		out["valkey"] = NewValkey(c)
	}
	return out
}

func each(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

// TestJoinOrderAndCapacity: callers queue in the order they join, a call that
// joins twice keeps its place, and a full queue refuses a new caller. It
// fails if the order depends on anything but the join time, a re-join moves
// the call, or the capacity is exceeded.
func TestJoinOrderAndCapacity(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		var first Entry
		for i, id := range []string{"c1", "c2", "c3"} {
			e, err := s.Join(ctx, Entry{CallID: id, Queue: "sales", Caller: "555", Node: "sip-1"}, 3)
			if err != nil {
				t.Fatalf("join %s: %v", id, err)
			}
			if i == 0 {
				first = e
			}
			time.Sleep(3 * time.Millisecond)
		}
		if _, err := s.Join(ctx, Entry{CallID: "c4", Queue: "sales"}, 3); !errors.Is(err, ErrFull) {
			t.Fatalf("a fourth caller joined a queue of 3: %v", err)
		}
		again, err := s.Join(ctx, Entry{CallID: "c1", Queue: "sales", Node: "sip-2"}, 3)
		if err != nil || again.EnteredMs != first.EnteredMs {
			t.Fatalf("re-join = %+v, %v; want the original entry %+v", again, err, first)
		}
		if _, err := s.Join(ctx, Entry{CallID: "o1", Queue: "support"}, 3); err != nil {
			t.Fatalf("another queue is not independent: %v", err)
		}
		ws, err := s.Waiting(ctx, "sales")
		if err != nil || len(ws) != 3 || ws[0].CallID != "c1" || ws[1].CallID != "c2" || ws[2].CallID != "c3" {
			t.Fatalf("waiting = %+v, %v", ws, err)
		}
		if err := s.Leave(ctx, "sales", "c2"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Join(ctx, Entry{CallID: "c5", Queue: "sales"}, 3); err != nil {
			t.Fatalf("a place freed by Leave was not reusable: %v", err)
		}
		ws, _ = s.Waiting(ctx, "sales")
		if Position(ws, "c3") != 2 || Position(ws, "c5") != 3 {
			t.Fatalf("positions after a leave: %+v", ws)
		}
		// Unlimited when max is 0.
		for i := 0; i < 10; i++ {
			if _, err := s.Join(ctx, Entry{CallID: fmt.Sprintf("u%d", i), Queue: "open"}, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// TestJoinKeepsEnteredTime: a taker that restores a dropped entry with the
// original time puts the caller back at the same place.
func TestJoinKeepsEnteredTime(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		a, _ := s.Join(ctx, Entry{CallID: "a", Queue: "q"}, 0)
		time.Sleep(3 * time.Millisecond)
		b, _ := s.Join(ctx, Entry{CallID: "b", Queue: "q"}, 0)
		if err := s.Leave(ctx, "q", "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Join(ctx, Entry{CallID: "a", Queue: "q", EnteredMs: a.EnteredMs}, 0); err != nil {
			t.Fatal(err)
		}
		ws, _ := s.Waiting(ctx, "q")
		if len(ws) != 2 || ws[0].CallID != "a" || ws[0].EnteredMs != a.EnteredMs || ws[1].EnteredMs != b.EnteredMs {
			t.Fatalf("restored order = %+v", ws)
		}
	})
}

// TestTouchAndOffering: a heartbeat records the owner and the agent being
// rung, and reports a vanished entry.
func TestTouchAndOffering(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if _, err := s.Join(ctx, Entry{CallID: "c", Queue: "q", Node: "sip-1"}, 0); err != nil {
			t.Fatal(err)
		}
		ok, err := s.Touch(ctx, "q", "c", "sip-2", "101")
		if err != nil || !ok {
			t.Fatalf("touch = %v, %v", ok, err)
		}
		ws, _ := s.Waiting(ctx, "q")
		if ws[0].Node != "sip-2" || ws[0].Offering != "101" {
			t.Fatalf("entry after touch = %+v", ws[0])
		}
		if ok, _ := s.Touch(ctx, "q", "nobody", "sip-2", ""); ok {
			t.Fatal("touch of a call that is not queued succeeded")
		}
	})
}

// TestLeaseExclusive: only one holder gets a lease, a stranger cannot release
// it, and it can be taken again after release.
func TestLeaseExclusive(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		key := AgentLease("101")
		if ok, err := s.Acquire(ctx, key, "node-a", time.Minute); err != nil || !ok {
			t.Fatalf("first acquire = %v, %v", ok, err)
		}
		if ok, _ := s.Acquire(ctx, key, "node-b", time.Minute); ok {
			t.Fatal("a held lease was acquired twice")
		}
		if h, ok, _ := s.Holder(ctx, key); !ok || h != "node-a" {
			t.Fatalf("holder = %q, %v", h, ok)
		}
		if err := s.Release(ctx, key, "node-b"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.Acquire(ctx, key, "node-b", time.Minute); ok {
			t.Fatal("a stranger's release freed the lease")
		}
		if err := s.Release(ctx, key, "node-a"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.Acquire(ctx, key, "node-b", time.Minute); !ok {
			t.Fatal("the lease was not free after release")
		}
		if _, ok, _ := s.Holder(ctx, AgentLease("nobody")); ok {
			t.Fatal("holder of a free lease")
		}
	})
}

// TestLeaseExpires: a lease nobody releases (its node died) frees itself.
func TestLeaseExpires(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if ok, _ := s.Acquire(ctx, CallLease("c"), "dead-node", 150*time.Millisecond); !ok {
			t.Fatal("acquire")
		}
		time.Sleep(300 * time.Millisecond)
		if ok, _ := s.Acquire(ctx, CallLease("c"), "live-node", time.Minute); !ok {
			t.Fatal("an expired lease was not free")
		}
	})
}

// TestTwoNodesNeverOfferTheSameCallOrAgent: several nodes race to offer
// waiting calls to a set of agents the way the SIP nodes do (the call's
// lease, then the agent's). It fails if a call is held by two nodes at once
// or an agent is rung for two calls at once.
func TestTwoNodesNeverOfferTheSameCallOrAgent(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const nodes, rounds = 4, 60
		agents := []string{"101", "102"}
		var (
			mu          sync.Mutex
			callHolders = map[string]int{}
			agentHold   = map[string]int{}
			offers      atomic.Int64
			bad         atomic.Int64
		)
		enter := func(m map[string]int, k string) bool {
			mu.Lock()
			defer mu.Unlock()
			m[k]++
			return m[k] == 1
		}
		leave := func(m map[string]int, k string) {
			mu.Lock()
			m[k]--
			mu.Unlock()
		}
		var wg sync.WaitGroup
		for n := 0; n < nodes; n++ {
			token := fmt.Sprintf("node-%d", n)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					call := fmt.Sprintf("call-%d", r%3) // the same few calls, offered by every node
					ok, err := s.Acquire(ctx, CallLease(call), token, 5*time.Second)
					if err != nil || !ok {
						continue
					}
					if !enter(callHolders, call) {
						bad.Add(1)
					}
					for _, a := range agents {
						got, err := s.Acquire(ctx, AgentLease(a), token, 5*time.Second)
						if err != nil || !got {
							continue
						}
						if !enter(agentHold, a) {
							bad.Add(1)
						}
						offers.Add(1)
						time.Sleep(time.Millisecond)
						leave(agentHold, a)
						_ = s.Release(ctx, AgentLease(a), token)
					}
					leave(callHolders, call)
					_ = s.Release(ctx, CallLease(call), token)
				}
			}()
		}
		wg.Wait()
		if bad.Load() != 0 {
			t.Fatalf("%d offers overlapped", bad.Load())
		}
		if offers.Load() == 0 {
			t.Fatal("no node ever got an offer through")
		}
	})
}

// TestAgentStateAndStats: login and pause state round-trips, the counters
// and wrap-up time accumulate, and the handling time averages.
func TestAgentStateAndStats(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if _, ok, err := s.Agent(ctx, "101"); err != nil || ok {
			t.Fatalf("unknown agent = %v, %v", ok, err)
		}
		st := AgentState{Ext: "101", LoggedIn: true, Queues: []string{"sales"}, Paused: true, Reason: "lunch", SinceMs: 42}
		if err := s.SetAgent(ctx, st); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.Agent(ctx, "101")
		if err != nil || !ok || got.Reason != "lunch" || !got.Paused || len(got.Queues) != 1 || got.SinceMs != 42 {
			t.Fatalf("agent = %+v, %v, %v", got, ok, err)
		}
		all, err := s.Agents(ctx)
		if err != nil || len(all) != 1 || all["101"].Ext != "101" {
			t.Fatalf("agents = %+v, %v", all, err)
		}

		if err := s.NoteAnswered(ctx, "sales", "101"); err != nil {
			t.Fatal(err)
		}
		if err := s.NoteAnswered(ctx, "sales", "101"); err != nil {
			t.Fatal(err)
		}
		before := time.Now()
		if err := s.NoteEnded(ctx, "sales", "101", 120*time.Second, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		stats, err := s.Stats(ctx, "sales")
		if err != nil {
			t.Fatal(err)
		}
		a := stats["101"]
		if a.Calls != 2 || a.LastEnd().Before(before.Add(-2*time.Second)) || !a.Wrapping(time.Now()) || a.Wrapping(time.Now().Add(40*time.Second)) {
			t.Fatalf("stats = %+v", a)
		}
		if other, _ := s.Stats(ctx, "support"); len(other) != 0 {
			t.Fatalf("stats leaked to another queue: %v", other)
		}
		avg, err := s.AvgHandle(ctx, "sales")
		if err != nil || avg != 120*time.Second {
			t.Fatalf("first average = %v, %v", avg, err)
		}
		if err := s.NoteEnded(ctx, "sales", "102", 220*time.Second, 0); err != nil {
			t.Fatal(err)
		}
		if avg, _ = s.AvgHandle(ctx, "sales"); avg != 140*time.Second {
			t.Fatalf("second average = %v, want 140s (weight 1/5)", avg)
		}
		if avg, _ = s.AvgHandle(ctx, "support"); avg != 0 {
			t.Fatalf("unmeasured average = %v", avg)
		}
	})
}

// TestRotationAdvances: the round robin counter advances once per call.
func TestRotationAdvances(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for want := uint64(0); want < 4; want++ {
			got, err := s.NextRotation(ctx, "sales")
			if err != nil || got != want {
				t.Fatalf("rotation = %d, %v; want %d", got, err, want)
			}
		}
		if got, _ := s.NextRotation(ctx, "support"); got != 0 {
			t.Fatalf("another queue's rotation = %d", got)
		}
	})
}

// TestStaleEntriesAreDropped: an entry whose owner stopped refreshing it
// disappears after EntryTTL unless a node adopts it first. It uses the
// in-process store's clock: the Valkey store prunes on Valkey's.
func TestStaleEntriesAreDropped(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	s := NewMemStoreClock(func() time.Time { return now })
	ctx := context.Background()
	if _, err := s.Join(ctx, Entry{CallID: "dead", Queue: "q", Node: "sip-1"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(ctx, Entry{CallID: "live", Queue: "q", Node: "sip-1"}, 0); err != nil {
		t.Fatal(err)
	}
	now = now.Add(EntryTTL - time.Second)
	if ok, _ := s.Touch(ctx, "q", "live", "sip-1", ""); !ok {
		t.Fatal("touch")
	}
	now = now.Add(2 * time.Second) // dead: past its TTL; live: refreshed 2 s ago
	ws, _ := s.Waiting(ctx, "q")
	if len(ws) != 1 || ws[0].CallID != "live" {
		t.Fatalf("waiting = %+v; the entry nobody refreshed must be dropped", ws)
	}
	// A dropped call's place is free and capacity counts only live entries.
	if _, err := s.Join(ctx, Entry{CallID: "new", Queue: "q"}, 2); err != nil {
		t.Fatalf("join with a stale entry present: %v", err)
	}
}
