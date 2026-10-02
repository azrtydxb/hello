package cdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var discard = slog.New(slog.DiscardHandler)

func count(c prometheus.Counter) float64 {
	var m dto.Metric
	_ = c.Write(&m)
	return m.GetCounter().GetValue()
}

type flakyDB struct {
	mu     sync.Mutex
	fail   int // fail this many calls first
	calls  int
	rows   []string
	unlock chan struct{}
}

func (f *flakyDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	if f.unlock != nil {
		<-f.unlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.fail {
		return pgconn.CommandTag{}, errors.New("database down")
	}
	f.rows = append(f.rows, args[0].(string))
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *flakyDB) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rows...)
}

func newCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: "hello_cdr_dropped_total"})
}

// TestWriterRetries fails if a record is lost while the database fails a
// few times, or written twice.
func TestWriterRetries(t *testing.T) {
	db := &flakyDB{fail: 3}
	w := NewWriter(db, newCounter(), discard)
	w.InitialBackoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, time.Second); close(done) }()
	w.Enqueue(Record{CorrelationID: "a"})
	w.Enqueue(Record{CorrelationID: "b"})
	deadline := time.Now().Add(5 * time.Second)
	for len(db.written()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if got := db.written(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("written = %v, want [a b] in order after retries", got)
	}
}

// TestWriterDropsWhenFull fails if Enqueue blocks or does not count a drop
// when the queue is full.
func TestWriterDropsWhenFull(t *testing.T) {
	dropped := newCounter()
	w := newWriter(&flakyDB{}, dropped, discard, 2)
	if !w.Enqueue(Record{CorrelationID: "1"}) || !w.Enqueue(Record{CorrelationID: "2"}) {
		t.Fatal("enqueue into a non-full queue failed")
	}
	start := time.Now()
	if w.Enqueue(Record{CorrelationID: "3"}) {
		t.Fatal("enqueue into a full queue succeeded")
	}
	if time.Since(start) > 50*time.Millisecond || count(dropped) != 1 {
		t.Fatalf("drop took %s, counted %v", time.Since(start), count(dropped))
	}
	if NewWriter(&flakyDB{}, dropped, discard).queue == nil || cap(NewWriter(&flakyDB{}, dropped, discard).queue) != QueueSize {
		t.Fatal("default queue size")
	}
}

// TestWriterFlushOnShutdown fails if queued records are not written after
// the context is cancelled.
func TestWriterFlushOnShutdown(t *testing.T) {
	db := &flakyDB{unlock: make(chan struct{})}
	w := NewWriter(db, newCounter(), discard)
	for i := range 3 {
		w.Enqueue(Record{CorrelationID: fmt.Sprint(i)})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	close(db.unlock)
	w.Run(ctx, time.Second)
	if got := db.written(); len(got) != 3 {
		t.Fatalf("flushed %v, want 3", got)
	}
}

// TestWriterPostgres fails if a record does not land in the cdrs table with
// NULL for unset times, or a retried record is inserted twice.
func TestWriterPostgres(t *testing.T) {
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
	defer func() { _ = admin.Close() }()
	name := fmt.Sprintf("hello_cdr_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") }()
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, _ := pgx.ParseConfig(u.String())
	sdb := stdlib.OpenDB(*scfg)
	if _, err := migrate.Up(ctx, sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	w := NewWriter(pool, newCounter(), discard)
	start := time.Now().Add(-10 * time.Second).UTC().Truncate(time.Millisecond)
	r := Record{CorrelationID: "corr-1", SIPCallID: "call-1", Source: "100", Destination: "200",
		StartTime: start, AnswerTime: start.Add(2 * time.Second), EndTime: start.Add(10 * time.Second),
		DurationMs: 10000, BillableMs: 8000, SIPNode: "sip-1", FinalStatus: 200, TerminationSide: SideCaller}
	if err := w.write(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := w.write(ctx, r); err != nil { // retry after an ambiguous failure
		t.Fatal(err)
	}
	var (
		n            int
		ring, answer *time.Time
		media, side  string
		billable     int64
	)
	if err := pool.QueryRow(ctx, "SELECT count(*), max(ring_time), max(answer_time), max(media_mode), max(termination_side), max(billable_ms) FROM cdrs").
		Scan(&n, &ring, &answer, &media, &side, &billable); err != nil {
		t.Fatal(err)
	}
	if n != 1 || ring != nil || answer == nil || !answer.Equal(r.AnswerTime) || media != "direct" || side != "caller" || billable != 8000 {
		t.Fatalf("row: n=%d ring=%v answer=%v media=%s side=%s billable=%d", n, ring, answer, media, side, billable)
	}
	var dir string
	if err := pool.QueryRow(ctx, "SELECT direction FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&dir); err != nil || dir != "internal" {
		t.Fatalf("default direction = %q, %v", dir, err)
	}

	// An outbound call's routing detail and trace.
	var tr routing.Trace
	tr.Add(`Route "UAE Mobile" matched (regex ^05[0-9]{8}$)`)
	tr.Add("carrier-backup -> 200 OK")
	out := Record{CorrelationID: "corr-2", SIPCallID: "call-2", Source: "100", Destination: "0501234567",
		StartTime: start, EndTime: start.Add(time.Second), SIPNode: "sip-1", FinalStatus: 200, TerminationSide: SideCaller,
		Direction: DirectionOutbound, OriginalDestination: "0501234567", RewrittenDestination: "+971501234567",
		Route: "UAE Mobile", Trunk: "carrier-backup", Trace: tr}
	if err := w.write(ctx, out); err != nil {
		t.Fatal(err)
	}
	var (
		orig, rewritten, route, trunk string
		raw                           []byte
	)
	if err := pool.QueryRow(ctx, `SELECT direction, original_destination, rewritten_destination, route_name, trunk_name, trace
		FROM cdrs WHERE correlation_id = 'corr-2'`).Scan(&dir, &orig, &rewritten, &route, &trunk, &raw); err != nil {
		t.Fatal(err)
	}
	var got routing.Trace
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if dir != "outbound" || orig != "0501234567" || rewritten != "+971501234567" || route != "UAE Mobile" || trunk != "carrier-backup" ||
		len(got) != 2 || got[1].N != 2 || got[1].Text != "carrier-backup -> 200 OK" {
		t.Fatalf("outbound row: %s %s %s %s %s %+v", dir, orig, rewritten, route, trunk, got)
	}
}
