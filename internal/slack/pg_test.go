package slack

// The PostgreSQL store against the real schema. The tests skip without
// HELLO_TEST_DATABASE_URL and each runs on its own migrated database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/migrate"
)

func scratch(t *testing.T) (*PG, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_slack_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*scfg)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	return &PG{DB: db}, db
}

// TestPGQueue fails if an event is queued twice for one destination, if the
// ingest transaction does not roll the posts back with the event, if a post
// is claimed twice, if the filters are ignored, or if the health, the
// disable rules and the retry of a dead post do not follow the Mem model.
func TestPGQueue(t *testing.T) {
	p, db := scratch(t)
	ctx := context.Background()
	mk := func(name string, evs, exts []string) Destination {
		id, err := p.NextID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		d, err := p.Create(ctx, "test", Destination{ID: id, Name: name, Mode: ModeWebhook, SecretEnc: []byte("sealed"), Events: evs, Extensions: exts, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	all := mk("all", EventTypes, nil)
	only := mk("only 300 missed", []string{events.CallMissed}, []string{"300"})
	if _, err := p.Create(ctx, "test", Destination{ID: 999, Name: "all", Mode: ModeBot, SecretEnc: []byte("x"), Events: EventTypes, Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	e := events.Entry{Cursor: "1-1", Event: events.Event{APIVersion: "1", ID: "evt_1", Type: events.CallMissed, CreatedAt: time.Now(),
		Data: events.Data{CallID: "c", Extension: "200", From: events.Party{Number: "+3250123456"}}}}
	tx, _ := db.BeginTx(ctx, nil)
	for range 2 {
		if err := p.Enqueue(ctx, tx, e); err != nil {
			t.Fatal(err)
		}
	}
	_ = tx.Rollback()
	if posts, _ := p.ListPosts(ctx, PostQuery{Limit: 10}); len(posts) != 0 {
		t.Fatalf("a rolled-back ingest left %d posts", len(posts))
	}
	for range 2 {
		tx, _ = db.BeginTx(ctx, nil)
		if err := p.Enqueue(ctx, tx, e); err != nil {
			t.Fatal(err)
		}
		_ = tx.Commit()
	}
	posts, _ := p.ListPosts(ctx, PostQuery{Limit: 10})
	if len(posts) != 1 || posts[0].DestinationID != all.ID {
		t.Fatalf("posts = %+v, want one for the destination without the extension filter", posts)
	}
	e2 := e
	e2.Type, e2.ID, e2.Data.Extension = events.CallRinging, "evt_2", "300"
	tx, _ = db.BeginTx(ctx, nil)
	_ = p.Enqueue(ctx, tx, e2)
	_ = tx.Commit()
	if posts, _ = p.ListPosts(ctx, PostQuery{Limit: 10}); len(posts) != 1 {
		t.Fatal("a call.ringing was queued")
	}

	now := time.Now()
	c1, err := p.Claim(ctx, now, 10, time.Minute)
	if err != nil || len(c1) != 1 || c1[0].Mode != ModeWebhook || string(c1[0].SecretEnc) != "sealed" {
		t.Fatalf("claim = %+v, %v", c1, err)
	}
	if c2, _ := p.Claim(ctx, now, 10, time.Minute); len(c2) != 0 {
		t.Fatal("a leased post was claimed twice")
	}
	// A retry makes it due later; a failure counts; success resets.
	if err := p.Finish(ctx, Finish{PostID: c1[0].ID, Now: now, Outcome: OutcomeRetry, Error: "http 503", NextAttemptAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if c, _ := p.Claim(ctx, now.Add(time.Minute), 10, time.Minute); len(c) != 0 {
		t.Fatal("a retry came back early")
	}
	c3, _ := p.Claim(ctx, now.Add(2*time.Hour), 10, time.Minute)
	if len(c3) != 1 || c3[0].SeriesAttempt != 1 || c3[0].Attempts != 1 {
		t.Fatalf("retry claim = %+v", c3)
	}
	d, _ := p.Get(ctx, all.ID)
	if d.ConsecutiveFailures != 1 || d.LastError != "http 503" || d.Pending != 1 {
		t.Fatalf("health = %+v", d)
	}
	if err := p.Finish(ctx, Finish{PostID: c3[0].ID, Now: now, Outcome: OutcomeDisable, Error: "Slack refused the credential", Reason: ReasonAuth}); err != nil {
		t.Fatal(err)
	}
	d, _ = p.Get(ctx, all.ID)
	if d.Enabled || d.DisabledReason != ReasonAuth || d.Dead != 1 || d.HasSecret != true {
		t.Fatalf("after a refused credential = %+v", d)
	}
	post, err := p.Retry(ctx, "test", c3[0].ID, now)
	if err != nil || post.Status != StatusPending {
		t.Fatalf("retry = %+v %v", post, err)
	}
	if _, err := p.Retry(ctx, "test", c3[0].ID, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("second retry: %v", err)
	}
	// A disabled destination's posts wait.
	if c, _ := p.Claim(ctx, now.Add(time.Hour), 10, time.Minute); len(c) != 0 {
		t.Fatal("a disabled destination was posted to")
	}
	// A new secret re-enables; a nil secret keeps the stored one.
	d.Enabled, d.DisabledReason, d.SecretEnc = true, "", []byte("new-sealed")
	if _, err := p.Update(ctx, "test", d, true); err != nil {
		t.Fatal(err)
	}
	d.SecretEnc = nil
	d.Name = "renamed"
	if _, err := p.Update(ctx, "test", d, false); err != nil {
		t.Fatal(err)
	}
	c4, _ := p.Claim(ctx, now.Add(time.Hour), 10, time.Minute)
	if len(c4) != 1 || string(c4[0].SecretEnc) != "new-sealed" {
		t.Fatalf("claim after update = %+v", c4)
	}
	if err := p.Finish(ctx, Finish{PostID: c4[0].ID, Now: now, Outcome: OutcomeSuccess}); err != nil {
		t.Fatal(err)
	}
	if d, _ = p.Get(ctx, all.ID); d.ConsecutiveFailures != 0 || d.LastSuccessAt == nil || d.LastError != "" {
		t.Fatalf("after success = %+v", d)
	}
	if n, err := p.Purge(ctx, now.Add(time.Hour), now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("purge = %d %v", n, err)
	}
	// Deleting a destination removes its posts.
	if err := p.Delete(ctx, "test", only.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "test", only.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
