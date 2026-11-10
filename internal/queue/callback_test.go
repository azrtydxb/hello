package queue

import (
	"errors"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClocked() (*MemCallbacks, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	m := NewMemCallbacks()
	m.Now = c.now
	return m, c
}

// TestCallbackStore: a request keeps the caller's place, a second request
// from the number coalesces, the queue holds at most 100, the head is the
// lowest place, a retry waits two minutes, the third failure ends the
// request, a lapsed lease is taken again (or fails once the attempts are
// used) and an old request expires. It fails if any of those rules changes.
func TestCallbackStore(t *testing.T) {
	ctx := t.Context()
	t.Run("coalesce and cap", func(t *testing.T) {
		m, _ := newClocked()
		a, created, err := m.Request(ctx, 1, "100", "Ann", 500)
		if err != nil || !created || a.PositionKey != 500 || a.Status != CallbackWaiting {
			t.Fatalf("first = %+v %v %v", a, created, err)
		}
		b, created, _ := m.Request(ctx, 1, "100", "", 900)
		if created || b.ID != a.ID || b.PositionKey != 500 {
			t.Fatalf("coalesced = %+v %v", b, created)
		}
		if _, created, _ := m.Request(ctx, 2, "100", "", 900); !created {
			t.Fatal("another queue must take its own request")
		}
		for i := 1; i < CallbackMaxWaiting; i++ {
			if _, _, err := m.Request(ctx, 1, "2"+itoa(i), "", int64(i)); err != nil {
				t.Fatalf("request %d: %v", i, err)
			}
		}
		if _, _, err := m.Request(ctx, 1, "999", "", 1); !errors.Is(err, ErrCallbackFull) {
			t.Fatalf("101st = %v, want ErrCallbackFull", err)
		}
	})
	t.Run("head is the lowest place", func(t *testing.T) {
		m, _ := newClocked()
		_, _, _ = m.Request(ctx, 1, "100", "", 300)
		_, _, _ = m.Request(ctx, 1, "101", "", 100)
		_, _, _ = m.Request(ctx, 1, "102", "", 200)
		c, ok, _ := m.Claim(ctx, 1, "n1")
		if !ok || c.Number != "101" || c.Attempts != 1 || c.Status != CallbackDialling {
			t.Fatalf("claim = %+v", c)
		}
		if due, _ := m.Due(ctx); due[1] != 100 {
			t.Fatalf("due = %v, want the claimed row's place 100 held", due)
		}
	})
	t.Run("retry delay and three attempts", func(t *testing.T) {
		m, clk := newClocked()
		_, _, _ = m.Request(ctx, 1, "100", "", 1)
		for i := 1; i <= CallbackMaxAttempts; i++ {
			c, ok, _ := m.Claim(ctx, 1, "n1")
			if !ok || c.Attempts != i {
				t.Fatalf("attempt %d: %+v %v", i, c, ok)
			}
			final, err := m.Fail(ctx, c.ID, "n1", "busy")
			if err != nil || final != (i == CallbackMaxAttempts) {
				t.Fatalf("fail %d: final=%v err=%v", i, final, err)
			}
			if _, ok, _ := m.Claim(ctx, 1, "n1"); ok {
				t.Fatal("claimed before the retry delay")
			}
			clk.t = clk.t.Add(CallbackRetryAfter)
		}
		rows, _ := m.List(ctx, 1)
		if rows[0].Status != CallbackFailed || rows[0].Result != "busy" {
			t.Fatalf("after three attempts: %+v", rows[0])
		}
		if _, ok, _ := m.Claim(ctx, 1, "n1"); ok {
			t.Fatal("a failed request was claimed")
		}
	})
	t.Run("lease", func(t *testing.T) {
		m, clk := newClocked()
		_, _, _ = m.Request(ctx, 1, "100", "", 1)
		c, _, _ := m.Claim(ctx, 1, "dead")
		if _, ok, _ := m.Claim(ctx, 1, "n2"); ok {
			t.Fatal("claimed under a live lease")
		}
		if err := m.Renew(ctx, c.ID, "dead"); err != nil {
			t.Fatal(err)
		}
		clk.t = clk.t.Add(CallbackLease + time.Second)
		c2, ok, _ := m.Claim(ctx, 1, "n2")
		if !ok || c2.ID != c.ID || c2.Attempts != 2 || c2.ClaimedBy != "n2" {
			t.Fatalf("reclaim = %+v %v", c2, ok)
		}
		if _, err := m.Fail(ctx, c.ID, "dead", "x"); !errors.Is(err, ErrCallbackNotFound) {
			t.Fatalf("a node that lost the claim ended it: %v", err)
		}
		// Used up: the third attempt's lease lapses and the request fails.
		_ = m.Renew(ctx, c.ID, "n2")
		m.rows[0].Attempts = CallbackMaxAttempts
		clk.t = clk.t.Add(CallbackLease + time.Second)
		if _, ok, _ := m.Claim(ctx, 1, "n3"); ok {
			t.Fatal("claimed after the attempts were used")
		}
		rows, _ := m.List(ctx, 1)
		if rows[0].Status != CallbackFailed || rows[0].Result != "node_lost" {
			t.Fatalf("exhausted = %+v", rows[0])
		}
	})
	t.Run("expiry, cancel, connect, purge", func(t *testing.T) {
		m, clk := newClocked()
		_, _, _ = m.Request(ctx, 1, "100", "", 1)
		c2, _, _ := m.Request(ctx, 1, "101", "", 2)
		c3, _, _ := m.Request(ctx, 1, "102", "", 3)
		if err := m.Cancel(ctx, 1, c2.ID); err != nil {
			t.Fatal(err)
		}
		if err := m.Cancel(ctx, 1, c2.ID); !errors.Is(err, ErrCallbackNotFound) {
			t.Fatalf("second cancel = %v", err)
		}
		if err := m.Cancel(ctx, 2, c3.ID); !errors.Is(err, ErrCallbackNotFound) {
			t.Fatalf("cancel through another queue = %v", err)
		}
		c, _, _ := m.Claim(ctx, 1, "n1")
		if err := m.Connect(ctx, c.ID, "n1", "200"); err != nil {
			t.Fatal(err)
		}
		clk.t = clk.t.Add(CallbackTTL + time.Second)
		rows, _ := m.List(ctx, 1)
		got := map[string]string{}
		for _, r := range rows {
			got[r.Number] = r.Status
		}
		if got["100"] != CallbackConnected || got["101"] != CallbackCancelled || got["102"] != CallbackExpired {
			t.Fatalf("statuses = %v", got)
		}
		clk.t = clk.t.Add(CallbackKeep + time.Second)
		_ = m.Purge(ctx)
		if rows, _ := m.List(ctx, 1); len(rows) != 0 {
			t.Fatalf("purge left %d rows", len(rows))
		}
	})
}

func TestAheadOfCallback(t *testing.T) {
	for name, tc := range map[string]struct {
		entries []Entry
		key     int64
		want    bool
	}{
		"empty":                      {nil, 100, false},
		"later caller":               {[]Entry{{EnteredMs: 200}}, 100, false},
		"earlier caller, unserved":   {[]Entry{{EnteredMs: 50}}, 100, true},
		"earlier caller, being rung": {[]Entry{{EnteredMs: 50, Offering: "200"}}, 100, false},
	} {
		if got := AheadOfCallback(tc.entries, tc.key); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
