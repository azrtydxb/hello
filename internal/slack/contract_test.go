package slack

// The Store contract: the same assertions against Mem (always) and PG (with
// HELLO_TEST_DATABASE_URL, on a migrated throwaway database). The service
// and worker tests run on Mem, so a PG change that Mem does not mirror (or
// the reverse) would pass them and still change what production does. The
// contract pins the behaviour both share: which calls report ErrNotFound
// and ErrConflict, the secret kept on a nil update, the queue filters and
// de-duplication, the claim lease, the health counters and the disable
// rules, the manual retry of a dead post, the post list filters and purge.
//
// Known differences the contract leaves out on purpose (Mem is a test
// double, not a second implementation): Mem's List orders by id, PG's by
// name; Mem keeps a nil Events or Extensions list nil where PG returns an
// empty one; Mem makes a queued post due at the Unix epoch, PG at the
// database's now(); Mem's Claim takes posts in the order they were queued,
// PG's by next attempt; Mem ignores the ingest transaction.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// contractRig is one Store under test and how to queue an event on it.
type contractRig struct {
	store   Store
	enqueue func(t *testing.T, e events.Entry)
}

func memContractRig(*testing.T) contractRig {
	m := NewMem()
	return contractRig{store: m, enqueue: func(t *testing.T, e events.Entry) {
		t.Helper()
		if err := m.Enqueue(context.Background(), nil, e); err != nil {
			t.Fatal(err)
		}
	}}
}

func pgContractRig(t *testing.T) contractRig {
	p, db := scratch(t)
	return contractRig{store: p, enqueue: func(t *testing.T, e events.Entry) {
		t.Helper()
		ctx := context.Background()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Enqueue(ctx, tx, e); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}}
}

func TestStoreContractMem(t *testing.T) { runStoreContract(t, memContractRig) }

func TestStoreContractPG(t *testing.T) { runStoreContract(t, pgContractRig) }

func runStoreContract(t *testing.T, newRig func(*testing.T) contractRig) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, r contractRig)
	}{
		{"Destinations", contractDestinations},
		{"Queue", contractQueue},
		{"FailureLimit", contractFailureLimit},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, newRig(t)) })
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

func mustCreate(t *testing.T, s Store, d Destination) Destination {
	t.Helper()
	id, err := s.NextID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d.ID = id
	out, err := s.Create(context.Background(), "test", d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func contractDestinations(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	a, err := s.NextID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.NextID(ctx)
	if a == b {
		t.Fatalf("NextID repeated %d", a)
	}
	d, err := s.Create(ctx, "alice", Destination{ID: a, Name: "alpha", Mode: ModeWebhook, SecretEnc: []byte("sealed"),
		Events: EventTypes, Extensions: []string{"200"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != a || d.Name != "alpha" || !d.HasSecret || string(d.SecretEnc) != "sealed" || d.CreatedBy != "alice" ||
		!d.Enabled || d.Pending != 0 || d.Dead != 0 || len(d.Events) != len(EventTypes) || len(d.Extensions) != 1 {
		t.Fatalf("created = %+v", d)
	}
	_, err = s.Create(ctx, "alice", Destination{ID: b, Name: "alpha", Mode: ModeBot, SecretEnc: []byte("x"), Enabled: true})
	wantErr(t, "duplicate name", err, ErrConflict)
	other := mustCreate(t, s, Destination{Name: "beta", Mode: ModeBot, SecretEnc: []byte("bot"), Channel: "#c", Enabled: true})

	_, err = s.Get(ctx, 99999)
	wantErr(t, "Get unknown", err, ErrNotFound)
	_, err = s.Update(ctx, "alice", Destination{ID: 99999, Name: "gamma", Mode: ModeWebhook}, false)
	wantErr(t, "Update unknown", err, ErrNotFound)
	upd := d
	upd.Name = "beta"
	_, err = s.Update(ctx, "alice", upd, false)
	wantErr(t, "Update to a taken name", err, ErrConflict)

	// A nil secret keeps the stored one; a new one replaces it.
	upd.Name, upd.SecretEnc, upd.Enabled, upd.DisabledReason = "alpha2", nil, false, ReasonAuth
	got, err := s.Update(ctx, "alice", upd, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "alpha2" || string(got.SecretEnc) != "sealed" || !got.HasSecret || got.Enabled || got.DisabledReason != ReasonAuth {
		t.Fatalf("after a nil-secret update = %+v", got)
	}
	upd.SecretEnc, upd.Enabled, upd.DisabledReason = []byte("new"), true, ""
	if got, err = s.Update(ctx, "alice", upd, true); err != nil || string(got.SecretEnc) != "new" || !got.Enabled {
		t.Fatalf("after a new secret = %+v, %v", got, err)
	}
	if got, err = s.Get(ctx, a); err != nil || got.Name != "alpha2" || string(got.SecretEnc) != "new" {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	names := map[string]bool{list[0].Name: true, list[1].Name: true}
	if !names["alpha2"] || !names[other.Name] {
		t.Fatalf("List names = %v", names)
	}

	if err := s.Delete(ctx, "alice", other.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "second Delete", s.Delete(ctx, "alice", other.ID), ErrNotFound)
	_, err = s.Get(ctx, other.ID)
	wantErr(t, "Get deleted", err, ErrNotFound)
}

func contractQueue(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	all := mustCreate(t, s, Destination{Name: "all", Mode: ModeWebhook, SecretEnc: []byte("sealed"), Events: EventTypes, Enabled: true})
	only := mustCreate(t, s, Destination{Name: "only 300", Mode: ModeBot, SecretEnc: []byte("bot"), Channel: "#c",
		Events: []string{events.CallMissed}, Extensions: []string{"300"}, Enabled: true})
	off := mustCreate(t, s, Destination{Name: "off", Mode: ModeWebhook, SecretEnc: []byte("x"), Events: EventTypes})

	// Filters and de-duplication: extension 200 matches only "all"; the
	// same event twice queues once; a type Slack does not post is ignored.
	e := missed("evt_1", "200")
	r.enqueue(t, e)
	r.enqueue(t, e)
	ringing := missed("evt_2", "300")
	ringing.Type = events.CallRinging
	r.enqueue(t, ringing)
	posts, err := s.ListPosts(ctx, PostQuery{Limit: 10})
	if err != nil || len(posts) != 1 || posts[0].DestinationID != all.ID || posts[0].EventID != "evt_1" ||
		posts[0].Status != StatusPending || posts[0].NextAttemptAt == nil {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	for _, d := range []Destination{only, off} {
		if p, _ := s.ListPosts(ctx, PostQuery{DestinationID: d.ID, Limit: 10}); len(p) != 0 {
			t.Fatalf("%s got posts %+v", d.Name, p)
		}
	}

	// The claim and its lease.
	now := time.Now().Add(time.Minute).Truncate(time.Second) // PostgreSQL keeps microseconds
	c1, err := s.Claim(ctx, now, 10, time.Minute)
	if err != nil || len(c1) != 1 || c1[0].Mode != ModeWebhook || string(c1[0].SecretEnc) != "sealed" || c1[0].SeriesAttempt != 0 {
		t.Fatalf("claim = %+v, %v", c1, err)
	}
	var stored events.Entry
	if err := json.Unmarshal(c1[0].Payload, &stored); err != nil || stored.ID != "evt_1" || stored.Data.Extension != "200" {
		t.Fatalf("payload = %s, %v", c1[0].Payload, err)
	}
	if c, _ := s.Claim(ctx, now, 10, time.Minute); len(c) != 0 {
		t.Fatal("a leased post was claimed twice")
	}
	wantErr(t, "Finish unknown", s.Finish(ctx, Finish{PostID: 99999, Now: now, Outcome: OutcomeSuccess}), ErrNotFound)

	// A retry is due later and counts a failure.
	if err := s.Finish(ctx, Finish{PostID: c1[0].ID, Now: now, Outcome: OutcomeRetry, Error: "http 503", NextAttemptAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.ListPosts(ctx, PostQuery{Status: StatusRetrying, Limit: 10}); len(p) != 1 || p[0].Attempts != 1 || p[0].LastError != "http 503" {
		t.Fatalf("retrying = %+v", p)
	}
	if c, _ := s.Claim(ctx, now.Add(2*time.Minute), 10, time.Minute); len(c) != 0 {
		t.Fatal("a retry came back early")
	}
	c2, _ := s.Claim(ctx, now.Add(2*time.Hour), 10, time.Minute)
	if len(c2) != 1 || c2[0].SeriesAttempt != 1 || c2[0].Attempts != 1 {
		t.Fatalf("retry claim = %+v", c2)
	}
	d, _ := s.Get(ctx, all.ID)
	if d.ConsecutiveFailures != 1 || d.LastError != "http 503" || d.LastFailureAt == nil || d.Pending != 1 {
		t.Fatalf("health = %+v", d)
	}

	// A refused credential kills the post and disables the destination.
	if err := s.Finish(ctx, Finish{PostID: c2[0].ID, Now: now, Outcome: OutcomeDisable, Error: "revoked", Reason: ReasonAuth}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Get(ctx, all.ID)
	if d.Enabled || d.DisabledReason != ReasonAuth || d.Dead != 1 || d.Pending != 0 || d.ConsecutiveFailures != 2 {
		t.Fatalf("after a refused credential = %+v", d)
	}

	// A manual retry of the dead post; a second one conflicts.
	post, err := s.Retry(ctx, "test", c2[0].ID, now.Add(3*time.Hour))
	if err != nil || post.Status != StatusPending || post.NextAttemptAt == nil {
		t.Fatalf("retry = %+v, %v", post, err)
	}
	_, err = s.Retry(ctx, "test", c2[0].ID, now)
	wantErr(t, "retry of a pending post", err, ErrConflict)
	_, err = s.Retry(ctx, "test", 99999, now)
	wantErr(t, "retry of an unknown post", err, ErrNotFound)
	if c, _ := s.Claim(ctx, now.Add(4*time.Hour), 10, time.Minute); len(c) != 0 {
		t.Fatal("a disabled destination was posted to")
	}

	// Re-enabled with a reset: the retried post goes out with a fresh
	// series, and success resets the health.
	d.Enabled, d.DisabledReason, d.SecretEnc = true, "", nil
	if d, err = s.Update(ctx, "test", d, true); err != nil || d.ConsecutiveFailures != 0 || d.LastError != "" {
		t.Fatalf("reset = %+v, %v", d, err)
	}
	c3, _ := s.Claim(ctx, now.Add(4*time.Hour), 10, time.Minute)
	if len(c3) != 1 || c3[0].SeriesAttempt != 0 || c3[0].Attempts != 2 || string(c3[0].SecretEnc) != "sealed" {
		t.Fatalf("claim after the retry = %+v", c3)
	}
	done := now.Add(4 * time.Hour)
	if err := s.Finish(ctx, Finish{PostID: c3[0].ID, Now: done, Outcome: OutcomeSuccess}); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.Get(ctx, all.ID); d.ConsecutiveFailures != 0 || d.LastSuccessAt == nil || d.LastError != "" || d.Pending != 0 {
		t.Fatalf("after success = %+v", d)
	}
	p, _ := s.ListPosts(ctx, PostQuery{DestinationID: all.ID, Status: StatusSucceeded, Limit: 10})
	if len(p) != 1 || p[0].DeliveredAt == nil || p[0].NextAttemptAt != nil || p[0].Attempts != 3 {
		t.Fatalf("succeeded = %+v", p)
	}

	// Paging and purge.
	r.enqueue(t, missed("evt_3", "200"))
	page, _ := s.ListPosts(ctx, PostQuery{Limit: 1})
	if len(page) != 1 || page[0].EventID != "evt_3" {
		t.Fatalf("first page = %+v", page)
	}
	if next, _ := s.ListPosts(ctx, PostQuery{Before: page[0].ID, Limit: 10}); len(next) != 1 || next[0].EventID != "evt_1" {
		t.Fatalf("next page = %+v", next)
	}
	if n, err := s.Purge(ctx, done, done); err != nil || n != 0 {
		t.Fatalf("purge before the cutoff = %d, %v", n, err)
	}
	if n, err := s.Purge(ctx, done.Add(time.Second), done.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if p, _ := s.ListPosts(ctx, PostQuery{Limit: 10}); len(p) != 1 || p[0].EventID != "evt_3" {
		t.Fatalf("after purge = %+v", p)
	}

	// Deleting a destination removes its posts.
	if err := s.Delete(ctx, "test", all.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.ListPosts(ctx, PostQuery{Limit: 10}); len(p) != 0 {
		t.Fatalf("posts of a deleted destination = %+v", p)
	}
}

// contractFailureLimit: reaching MaxFailures disables the destination with
// ReasonFailures; a dead post below it does not.
func contractFailureLimit(t *testing.T, r contractRig) {
	ctx, s := context.Background(), r.store
	d := mustCreate(t, s, Destination{Name: "flaky", Mode: ModeWebhook, SecretEnc: []byte("s"), Events: EventTypes, Enabled: true})
	now := time.Now().Add(time.Minute).Truncate(time.Second) // PostgreSQL keeps microseconds
	for i, id := range []string{"evt_a", "evt_b"} {
		r.enqueue(t, missed(id, "200"))
		c, err := s.Claim(ctx, now, 10, time.Minute)
		if err != nil || len(c) != 1 {
			t.Fatalf("claim %d = %+v, %v", i, c, err)
		}
		if err := s.Finish(ctx, Finish{PostID: c[0].ID, Now: now, Outcome: OutcomeDead, Error: "gone", MaxFailures: 2}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(ctx, d.ID)
		if i == 0 && (!got.Enabled || got.DisabledReason != "" || got.Dead != 1) {
			t.Fatalf("after one failure = %+v", got)
		}
		if i == 1 && (got.Enabled || got.DisabledReason != ReasonFailures || got.Dead != 2) {
			t.Fatalf("after two failures = %+v", got)
		}
	}
}
