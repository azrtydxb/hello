package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/azrtydxb/hello/internal/queue"
)

// TestQueueCallbacksStore runs the callback request rules against
// PostgreSQL: a request keeps its place, a second one from the number
// coalesces, a queue holds at most 100, the head is the lowest place, two
// claims never take one row, a lapsed lease is taken again, three failures
// end the request, and a request past its time expires. It fails if the
// database's rules differ from queue.MemCallbacks'.
func TestQueueCallbacksStore(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	q, err := s.CreateQueue(ctx, "t", queueInput("support", "800"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cb := s.QueueCallbacks()

	a, created, err := cb.Request(ctx, q.ID, "0471234567", "Ann", 500)
	if err != nil || !created || a.PositionKey != 500 || a.Status != queue.CallbackWaiting {
		t.Fatalf("request = %+v %v %v", a, created, err)
	}
	b, created, err := cb.Request(ctx, q.ID, "0471234567", "", 900)
	if err != nil || created || b.ID != a.ID || b.PositionKey != 500 {
		t.Fatalf("coalesced = %+v %v %v", b, created, err)
	}
	if _, _, err := cb.Request(ctx, q.ID, "0472000000", "", 100); err != nil {
		t.Fatal(err)
	}
	head, ok, err := cb.Head(ctx, q.ID)
	if err != nil || !ok || head.Number != "0472000000" {
		t.Fatalf("head = %+v %v %v, want the lowest place", head, ok, err)
	}
	due, err := cb.Due(ctx)
	if err != nil || due[q.ID] != 100 {
		t.Fatalf("due = %v %v", due, err)
	}

	// Two nodes claim at once: one row each, never the same.
	got := make(chan queue.Callback, 2)
	for _, node := range []string{"n1", "n2"} {
		go func() {
			c, _, _ := cb.Claim(ctx, q.ID, node)
			got <- c
		}()
	}
	c1, c2 := <-got, <-got
	if c1.ID == 0 || c2.ID == 0 || c1.ID == c2.ID {
		t.Fatalf("claims = %+v and %+v, want two different rows", c1, c2)
	}
	if n, _ := cb.Dialling(ctx, q.ID); n != 2 {
		t.Fatalf("dialling = %d", n)
	}
	// A claim under a live lease is not taken; a lapsed one is, and counts
	// as an attempt.
	if _, ok, _ := cb.Claim(ctx, q.ID, "n3"); ok {
		t.Fatal("claimed under a live lease")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE queue_callbacks SET lease_until = now() - interval '1 second' WHERE id = $1`, c1.ID); err != nil {
		t.Fatal(err)
	}
	re, ok, err := cb.Claim(ctx, q.ID, "n3")
	if err != nil || !ok || re.ID != c1.ID || re.Attempts != 2 || re.ClaimedBy != "n3" {
		t.Fatalf("reclaim = %+v %v %v", re, ok, err)
	}
	if _, err := cb.Fail(ctx, c1.ID, "n1", "busy"); !errors.Is(err, queue.ErrCallbackNotFound) {
		t.Fatalf("a node that lost the claim ended it: %v", err)
	}
	if err := cb.Renew(ctx, c1.ID, "n4"); err != nil {
		t.Fatal(err)
	}
	// Retry, then the third failure ends it.
	for i := 2; i <= queue.CallbackMaxAttempts; i++ {
		final, err := cb.Fail(ctx, c1.ID, map[bool]string{true: "n4", false: "n5"}[i == 2], "busy")
		if err != nil || final != (i == queue.CallbackMaxAttempts) {
			t.Fatalf("fail %d: final=%v err=%v", i, final, err)
		}
		if i == queue.CallbackMaxAttempts {
			break
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE queue_callbacks SET next_attempt_at = now() WHERE id = $1`, c1.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := cb.Claim(ctx, q.ID, "n5"); !ok {
			t.Fatal("the retry was not claimable")
		}
	}
	rows, err := cb.List(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]queue.Callback{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if r := byID[c1.ID]; r.Status != queue.CallbackFailed || r.Attempts != 3 || r.Result != "busy" || r.FinishedAt.IsZero() {
		t.Fatalf("after three failures: %+v", r)
	}

	// Connect, cancel, expire.
	if err := cb.Connect(ctx, c2.ID, c2.ClaimedBy, "200"); err != nil {
		t.Fatal(err)
	}
	x, _, _ := cb.Request(ctx, q.ID, "0473000000", "", 700)
	if err := cb.Cancel(ctx, q.ID, x.ID); err != nil {
		t.Fatal(err)
	}
	if err := cb.Cancel(ctx, q.ID, x.ID); !errors.Is(err, queue.ErrCallbackNotFound) {
		t.Fatalf("second cancel = %v", err)
	}
	y, _, _ := cb.Request(ctx, q.ID, "0474000000", "", 800)
	if _, err := s.db.ExecContext(ctx, `UPDATE queue_callbacks SET expires_at = now() - interval '1 second' WHERE id = $1`, y.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cb.Head(ctx, q.ID); ok {
		t.Fatal("an expired request is due")
	}
	rows, _ = cb.List(ctx, q.ID)
	for _, r := range rows {
		if r.ID == y.ID && r.Status != queue.CallbackExpired {
			t.Fatalf("expired request is %s", r.Status)
		}
		if r.ID == c2.ID && (r.Status != queue.CallbackConnected || r.Agent != "200") {
			t.Fatalf("connected request = %+v", r)
		}
	}
	// Purge keeps recent rows and drops old finished ones.
	if _, err := s.db.ExecContext(ctx, `UPDATE queue_callbacks SET finished_at = now() - interval '8 days' WHERE id = $1`, y.ID); err != nil {
		t.Fatal(err)
	}
	if err := cb.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ = cb.List(ctx, q.ID)
	for _, r := range rows {
		if r.ID == y.ID {
			t.Fatal("an old finished request survived the purge")
		}
	}
	if len(rows) != 3 {
		t.Fatalf("rows after purge = %d, want 3", len(rows))
	}
}

// TestQueueCallbacksCap: the 101st open request of a queue is refused.
func TestQueueCallbacksCap(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	q, err := s.CreateQueue(ctx, "t", queueInput("support", "800"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cb := s.QueueCallbacks()
	for i := 0; i < queue.CallbackMaxWaiting; i++ {
		if _, _, err := cb.Request(ctx, q.ID, fmt.Sprintf("04%08d", i), "", int64(i)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, _, err := cb.Request(ctx, q.ID, "0499999999", "", 1); !errors.Is(err, queue.ErrCallbackFull) {
		t.Fatalf("101st request = %v, want ErrCallbackFull", err)
	}
	// A caller with an open request still coalesces at the cap.
	if _, created, err := cb.Request(ctx, q.ID, "0400000000", "", 1); err != nil || created {
		t.Fatalf("coalesce at the cap: created=%v err=%v", created, err)
	}
}

// TestQueueCallbackSettings: the callback settings and the prompt language
// are stored and read back, and an empty language is stored as inherit. It
// fails if a setting is dropped or the digit default is not applied.
func TestQueueCallbackSettings(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	in := queueInput("support", "800")
	in.CallbackEnabled, in.CallbackOfferAfter, in.CallbackDigit, in.PromptLanguage = true, 45, "", "nl"
	q, err := s.CreateQueue(ctx, "t", in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.CallbackEnabled || q.CallbackOfferAfter != 45 || q.CallbackDigit != "2" || q.PromptLanguage != "nl" {
		t.Fatalf("queue = %+v", q)
	}
	langs, err := s.QueuePromptLanguages(ctx)
	if err != nil || len(langs) != 1 || langs[0] != "nl" {
		t.Fatalf("languages = %v %v", langs, err)
	}
	in.PromptLanguage = ""
	q, err = s.UpdateQueue(ctx, "t", q.ID, in, nil)
	if err != nil || q.PromptLanguage != "" {
		t.Fatalf("cleared = %+v %v", q, err)
	}
}
