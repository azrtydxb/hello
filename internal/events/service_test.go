package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/valkey-io/valkey-go"
)

// pipeline is a Service on a scratch database and the CI Valkey (database 2,
// this package's; TestValkeyDBsPerPackage), skipping without them.
type pipeline struct {
	svc *Service
	vk  valkey.Client
	db  *sql.DB
	app ValkeyAppender
}

func newPipeline(t *testing.T) *pipeline {
	t.Helper()
	dsn, addr := os.Getenv("HELLO_TEST_DATABASE_URL"), os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("HELLO_TEST_DATABASE_URL and HELLO_TEST_VALKEY_ADDR not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_events_%d", time.Now().UnixNano())
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
	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 2}) // events' DB (TestValkeyDBsPerPackage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vk.Close)
	if err := vk.Do(ctx, vk.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{DB: db, Valkey: func() valkey.Client { return vk }, Consumer: "test-1", Log: discard,
		ClaimIdle: 200 * time.Millisecond, ClaimEvery: 100 * time.Millisecond}
	return &pipeline{svc: svc, vk: vk, db: db, app: ValkeyAppender{C: vk}}
}

// run starts the service until the test ends.
func (p *pipeline) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// jsonOf encodes an event as the stream stores it.
func jsonOf(e Event) ([]byte, error) { return json.Marshal(e) }

func (p *pipeline) emit(t *testing.T, e Event) {
	t.Helper()
	b, err := jsonOf(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.app.Append(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func (p *pipeline) stored(t *testing.T, q Query) []Entry {
	t.Helper()
	es, err := p.svc.History(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func (p *pipeline) waitStored(t *testing.T, n int) []Entry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		es := p.stored(t, Query{Limit: MaxLimit})
		if len(es) >= n {
			return es
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d events stored, want %d", len(es), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *pipeline) pending(t *testing.T) int64 {
	t.Helper()
	msg, err := p.vk.Do(context.Background(), p.vk.B().Arbitrary("XPENDING").Keys(StreamKey).Args(Group).Build()).ToArray()
	if err != nil {
		t.Fatal(err)
	}
	n, _ := msg[0].AsInt64()
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestIngestStoresOnceAndAcknowledges fails if an event is not stored with
// the stream id as its cursor, if the same event appended twice is stored
// twice or runs the hooks twice, if an entry that is not an event is left
// unacknowledged, or if a failing hook loses the event instead of retrying.
func TestIngestStoresOnceAndAcknowledges(t *testing.T) {
	p := newPipeline(t)
	var mu sync.Mutex
	var hooked []string
	failOnce := true
	p.svc.Hooks = []TxHook{func(_ context.Context, _ *sql.Tx, e Entry) error {
		mu.Lock()
		defer mu.Unlock()
		if e.Data.Extension == "300" && failOnce {
			failOnce = false
			return fmt.Errorf("hook failed")
		}
		hooked = append(hooked, e.ID)
		return nil
	}}
	p.run(t)

	a, dup, retried := ev(CallRinging, "200"), ev(CallEnded, "200"), ev(CallMissed, "300")
	p.emit(t, a)
	p.emit(t, dup)
	p.emit(t, dup) // at-least-once delivery: the same event id again
	if err := p.vk.Do(context.Background(), p.vk.B().Arbitrary("XADD").Keys(StreamKey).Args("*", "e", "not json").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	p.emit(t, retried)
	es := p.waitStored(t, 3)
	if len(es) != 3 {
		t.Fatalf("stored %d events, want 3 (one duplicate)", len(es))
	}
	for _, e := range es {
		if c, err := ParseCursor(e.Cursor); err != nil || c.Zero() {
			t.Fatalf("cursor %q", e.Cursor)
		}
	}
	if es[0].ID != a.ID || es[1].ID != dup.ID || es[2].ID != retried.ID {
		t.Fatalf("stored out of order: %s %s %s", es[0].Type, es[1].Type, es[2].Type)
	}
	waitFor(t, "every entry acknowledged", func() bool { return p.pending(t) == 0 })
	mu.Lock()
	defer mu.Unlock()
	// A failing hook rolls its batch back, so the events before it in the
	// batch run their hooks again: a hook's work belongs in the transaction.
	// What must hold: every event is hooked, and the one whose hook failed
	// once was hooked exactly once more (the retry), never for the duplicate.
	count := map[string]int{}
	for _, id := range hooked {
		count[id]++
	}
	if count[a.ID] == 0 || count[dup.ID] == 0 || count[retried.ID] != 1 {
		t.Fatalf("hooks ran %v, want every event hooked and %s exactly once", count, retried.ID)
	}
	if len(count) != 3 {
		t.Fatalf("hooks ran for %d distinct events, want 3", len(count))
	}
}

// TestIngestReclaimsACrashedConsumer fails if an entry another replica read
// and never acknowledged (it crashed) is not taken over and stored.
func TestIngestReclaimsACrashedConsumer(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()
	if err := p.vk.Do(ctx, p.vk.B().Arbitrary("XGROUP").Args("CREATE", StreamKey, Group, "0", "MKSTREAM").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	lost := ev(CallEnded, "100")
	p.emit(t, lost)
	// A consumer named "dead" reads the entry and never acknowledges it.
	if err := p.vk.Do(ctx, p.vk.B().Arbitrary("XREADGROUP").Args("GROUP", Group, "dead", "COUNT", "10", "STREAMS").Keys(StreamKey).Args(">").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if n := p.pending(t); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
	p.run(t)
	es := p.waitStored(t, 1)
	if es[0].ID != lost.ID {
		t.Fatalf("stored %s", es[0].ID)
	}
	waitFor(t, "the claimed entry acknowledged", func() bool { return p.pending(t) == 0 })
}

// TestHistoryFilters fails if the history ignores a type, prefix, extension
// or allowed-extension filter, a cursor bound or the order, or returns more
// than the limit.
func TestHistoryFilters(t *testing.T) {
	p := newPipeline(t)
	p.run(t)
	events := []Event{ev(CallRinging, "200"), ev(CallAnswered, "200"), ev(CallRinging, "201"), ev(CallEnded, "201"), ev(CallRecordingReady, "200")}
	for _, e := range events {
		p.emit(t, e)
	}
	all := p.waitStored(t, len(events))
	types := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Type+"/"+e.Data.Extension)
		}
		return out
	}
	for name, tc := range map[string]struct {
		q    Query
		want []string
	}{
		"exact":    {Query{Filter: Filter{Types: []string{CallRinging}}}, []string{"call.ringing/200", "call.ringing/201"}},
		"prefix":   {Query{Filter: Filter{Types: []string{"call.recording.*"}}}, []string{"call.recording.ready/200"}},
		"mixed":    {Query{Filter: Filter{Types: []string{CallEnded, "call.recording.*"}}}, []string{"call.ended/201", "call.recording.ready/200"}},
		"ext":      {Query{Filter: Filter{Extension: "201"}}, []string{"call.ringing/201", "call.ended/201"}},
		"allowed":  {Query{Filter: Filter{Allowed: []string{"201", "999"}}}, []string{"call.ringing/201", "call.ended/201"}},
		"nobody":   {Query{Filter: Filter{Allowed: []string{}}}, nil},
		"after":    {Query{After: mustCursor(t, all[2].Cursor)}, []string{"call.ended/201", "call.recording.ready/200"}},
		"before":   {Query{Before: mustCursor(t, all[2].Cursor)}, []string{"call.ringing/200", "call.answered/200"}},
		"desc":     {Query{Desc: true, Limit: 2}, []string{"call.recording.ready/200", "call.ended/201"}},
		"desc bef": {Query{Desc: true, Before: mustCursor(t, all[2].Cursor), Limit: 5}, []string{"call.answered/200", "call.ringing/200"}},
		"limit":    {Query{Limit: 2}, []string{"call.ringing/200", "call.answered/200"}},
	} {
		if got := types(p.stored(t, tc.q)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func mustCursor(t *testing.T, s string) Cursor {
	t.Helper()
	c, err := ParseCursor(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func replayed(t *testing.T, p *pipeline, after Cursor, f Filter) []string {
	t.Helper()
	var got []string
	err := p.svc.Replay(context.Background(), after, f, func(e Entry) error {
		got = append(got, e.Type+"/"+e.Data.Extension)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestReplayJoinsDatabaseAndStream fails if a resume loses or repeats an
// event when some are only in the database (trimmed from the stream), some in
// both, and some only in the stream (not ingested yet), or ignores the
// cursor or the filter.
func TestReplayJoinsDatabaseAndStream(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()
	// Nothing yet: an empty stream and database replay nothing.
	if got := replayed(t, p, Cursor{}, Filter{}); len(got) != 0 {
		t.Fatalf("empty replay = %v", got)
	}
	// Two events stored by the ingester, then trimmed from the stream; one
	// stored and still in the stream; two more only in the stream.
	stop := make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { p.svc.Run(runCtx); close(done) }()
	p.emit(t, ev(CallRinging, "200"))
	p.emit(t, ev(CallAnswered, "200"))
	p.emit(t, ev(CallEnded, "200"))
	es := p.waitStored(t, 3)
	cancel()
	<-done
	close(stop)
	// MAXLEN = 1 keeps only the newest entry (call.ended).
	if err := p.vk.Do(ctx, p.vk.B().Arbitrary("XTRIM").Keys(StreamKey).Args("MAXLEN", "1").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	p.emit(t, ev(CallMissed, "201"))
	p.emit(t, ev(CallCompleted, "200"))

	want := []string{"call.ringing/200", "call.answered/200", "call.ended/200", "call.missed/201", "call.completed/200"}
	if got := replayed(t, p, Cursor{}, Filter{}); !slices.Equal(got, want) {
		t.Fatalf("replay from the start = %v, want %v", got, want)
	}
	if got := replayed(t, p, mustCursor(t, es[0].Cursor), Filter{}); !slices.Equal(got, want[1:]) {
		t.Fatalf("replay after the first = %v, want %v", got, want[1:])
	}
	// After the entry the stream still holds: stream only.
	if got := replayed(t, p, mustCursor(t, es[2].Cursor), Filter{}); !slices.Equal(got, want[3:]) {
		t.Fatalf("replay after the oldest stream entry = %v, want %v", got, want[3:])
	}
	if got := replayed(t, p, Cursor{}, Filter{Allowed: []string{"201"}}); !slices.Equal(got, []string{"call.missed/201"}) {
		t.Fatalf("filtered replay = %v", got)
	}
	if got := replayed(t, p, Cursor{}, Filter{Types: []string{CallCompleted}}); !slices.Equal(got, []string{"call.completed/200"}) {
		t.Fatalf("type-filtered replay = %v", got)
	}
	// fn failing stops the replay and returns the error.
	stopErr := fmt.Errorf("client gone")
	n := 0
	if err := p.svc.Replay(ctx, Cursor{}, Filter{}, func(Entry) error { n++; return stopErr }); !errors.Is(err, stopErr) || n != 1 {
		t.Fatalf("Replay = %v after %d events, want %v after 1", err, n, stopErr)
	}
}

// TestLiveFanOut fails if a subscriber does not receive an event appended
// after it subscribed, in order, with the stream id as its cursor, or if a
// subscriber with a restricted extension set receives another extension's.
func TestLiveFanOut(t *testing.T) {
	p := newPipeline(t)
	p.run(t)
	all := p.svc.Subscribe(Filter{})
	own := p.svc.Subscribe(Filter{Allowed: []string{"200"}})
	defer all.Close()
	defer own.Close()
	// The live reader starts from the stream's tail: let it start first.
	time.Sleep(300 * time.Millisecond)
	p.emit(t, ev(CallRinging, "200"))
	p.emit(t, ev(CallRinging, "201"))
	p.emit(t, ev(CallEnded, "200"))
	var got []string
	for len(got) < 3 {
		select {
		case e := <-all.C:
			got = append(got, e.Type+"/"+e.Data.Extension)
			if _, err := ParseCursor(e.Cursor); err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("live events = %v", got)
		}
	}
	if !slices.Equal(got, []string{"call.ringing/200", "call.ringing/201", "call.ended/200"}) {
		t.Fatalf("live order = %v", got)
	}
	var mine []string
	for len(mine) < 2 {
		select {
		case e := <-own.C:
			mine = append(mine, e.Type+"/"+e.Data.Extension)
		case <-time.After(10 * time.Second):
			t.Fatalf("restricted events = %v", mine)
		}
	}
	if !slices.Equal(mine, []string{"call.ringing/200", "call.ended/200"}) {
		t.Fatalf("restricted subscriber got %v", mine)
	}
}

// TestTicketIsSingleUse fails if a ticket redeems twice, or expires never.
func TestTicketIsSingleUse(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()
	if err := p.svc.PutTicket(ctx, "h1", []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	b, ok, err := p.svc.TakeTicket(ctx, "h1")
	if err != nil || !ok || string(b) != `{"x":1}` {
		t.Fatalf("first redeem = %s, %v, %v", b, ok, err)
	}
	if _, ok, err := p.svc.TakeTicket(ctx, "h1"); err != nil || ok {
		t.Fatalf("second redeem = %v, %v", ok, err)
	}
	if _, ok, _ := p.svc.TakeTicket(ctx, "never-minted"); ok {
		t.Fatal("an unknown ticket redeemed")
	}
	if err := p.svc.PutTicket(ctx, "h2", []byte("x")); err != nil {
		t.Fatal(err)
	}
	ttl, err := p.vk.Do(ctx, p.vk.B().Ttl().Key(ticketKey+"h2").Build()).AsInt64()
	if err != nil || ttl < 1 || ttl > int64(TicketTTL/time.Second) {
		t.Fatalf("ticket ttl = %d, %v", ttl, err)
	}
}

type lockerFunc func(context.Context, string) (func(), bool, error)

func (f lockerFunc) TryLock(ctx context.Context, key string) (func(), bool, error) {
	return f(ctx, key)
}

// TestPurge fails if the retention job keeps an event past its retention,
// deletes a fresh one, or runs on a replica that does not hold the lock.
func TestPurge(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()
	p.svc.RetentionDays = 3
	for i, age := range []time.Duration{10 * 24 * time.Hour, 2 * 24 * time.Hour, time.Hour} {
		e := ev(CallEnded, "100")
		e.ID = fmt.Sprintf("evt_%d", i)
		e.CreatedAt = time.Now().Add(-age)
		if _, err := p.db.ExecContext(ctx, `INSERT INTO integration_events (id, cursor_ms, cursor_seq, type, created_at, payload)
			VALUES ($1, $2, 0, 'call.ended', $3, '{}')`, e.ID, i+1, e.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	held := true
	p.svc.Locker = lockerFunc(func(context.Context, string) (func(), bool, error) { return func() {}, !held, nil })
	p.svc.purge(ctx)
	var n int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM integration_events`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("purge without the lock left %d rows (%v), want 3", n, err)
	}
	held = false
	p.svc.purge(ctx)
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM integration_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	if !slices.Equal(ids, []string{"evt_1", "evt_2"}) {
		t.Fatalf("after purge: %v, want [evt_1 evt_2]", ids)
	}
}
