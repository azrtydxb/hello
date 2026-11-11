package cdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
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
	var qp, ql *int64
	var qj *float64
	if err := pool.QueryRow(ctx, "SELECT rtp_packets, rtp_lost, rtp_jitter_ms FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&qp, &ql, &qj); err != nil || qp != nil || ql != nil || qj != nil {
		t.Fatalf("unmeasured quality = %v %v %v, %v", qp, ql, qj, err)
	}
	var cc, ce *string
	if err := pool.QueryRow(ctx, "SELECT caller_codec, callee_codec FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&cc, &ce); err != nil || cc != nil || ce != nil {
		t.Fatalf("codecs of a record without any = %v %v, %v, want NULL", cc, ce, err)
	}
	codec := Record{CorrelationID: "corr-codec", SIPCallID: "call-codec", StartTime: start, EndTime: start.Add(time.Second),
		SIPNode: "sip-1", FinalStatus: 200, TerminationSide: SideCaller, CallerCodec: "opus/48000", CalleeCodec: "PCMU/8000"}
	if err := w.write(ctx, codec); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT caller_codec, callee_codec FROM cdrs WHERE correlation_id = 'corr-codec'").Scan(&cc, &ce); err != nil ||
		cc == nil || ce == nil || *cc != "opus/48000" || *ce != "PCMU/8000" {
		t.Fatalf("stored codecs = %v %v, %v", cc, ce, err)
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

type argsDB struct{ args []any }

// TestAnsweredByCDR fails if the assistant's extension is not the insert's
// answered_by argument (the column after peer_call_id, migration 00065).
func TestAnsweredByCDR(t *testing.T) {
	db := &argsDB{}
	w := NewWriter(db, newCounter(), discard)
	now := time.Now()
	if err := w.write(context.Background(), Record{CorrelationID: "b", StartTime: now, EndTime: now, AnsweredBy: "300"}); err != nil {
		t.Fatal(err)
	}
	if got := db.args[32]; got != "300" {
		t.Fatalf("last insert argument = %#v, want the assistant's extension", got)
	}
}

func (a *argsDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	a.args = args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

// TestCallQualityCDR fails if the three quality columns are not at their
// positions in the insert arguments (the SRTP column rides last), carry anything but the record's values, or are non-NULL
// for a record without quality (directly-media or unanswered call).
func TestCallQualityCDR(t *testing.T) {
	db := &argsDB{}
	w := NewWriter(db, newCounter(), discard)
	now := time.Now()
	base := Record{CorrelationID: "q", StartTime: now, EndTime: now}

	if err := w.write(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if len(db.args) != 37 {
		t.Fatalf("insert takes %d arguments, want 37", len(db.args))
	}
	if db.args[33] != SRTPNone {
		t.Fatalf("srtp = %v, want the default %q", db.args[33], SRTPNone)
	}
	for i, a := range db.args[21:24] {
		if v, ok := a.(*int64); (ok && v != nil) || (!ok && a != (*float64)(nil)) {
			t.Fatalf("argument %d = %#v, want a nil pointer", 22+i, a)
		}
	}

	p, l, j := int64(1000), int64(12), 37.5
	base.RTPPackets, base.RTPLost, base.RTPJitterMs = &p, &l, &j
	if err := w.write(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if db.args[24] != "call" {
		t.Fatalf("kind = %v, want the default call", db.args[24])
	}
	if db.args[25] != "" || db.args[26] != "" {
		t.Fatalf("ring tag = %v (%v), want empty for a call that rang no device", db.args[25], db.args[26])
	}
	base.RingTag, base.RingRule = "vip", "rule:1 boss"
	if err := w.write(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if db.args[25] != "vip" || db.args[26] != "rule:1 boss" {
		t.Fatalf("ring tag = %v (%v), want vip (rule:1 boss)", db.args[25], db.args[26])
	}
	gp, gl, gj := db.args[21].(*int64), db.args[22].(*int64), db.args[23].(*float64)
	if *gp != 1000 || *gl != 12 || *gj != 37.5 {
		t.Fatalf("quality = %d, %d, %v", *gp, *gl, *gj)
	}
}

// TestFollowMeLegsCDR fails if the follow-me legs are not the last insert
// argument, as a JSON array (empty for a call with none), or if the statement
// does not write them to cdr_legs with the role follow_me.
func TestFollowMeLegsCDR(t *testing.T) {
	db := &argsDB{}
	w := NewWriter(db, newCounter(), discard)
	now := time.Now()
	r := Record{CorrelationID: "fm", StartTime: now, EndTime: now}
	if err := w.write(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got := db.args[36]; got != "[]" {
		t.Fatalf("follow-me legs = %v, want [] for a call with none", got)
	}
	r.FollowMeLegs = []FollowMeLeg{
		{Kind: "own", Extension: "101", Result: "no_answer"},
		{Kind: "external", Trunk: "carrier", Result: "answered"},
	}
	if err := w.write(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var got []FollowMeLeg
	if err := json.Unmarshal([]byte(db.args[36].(string)), &got); err != nil || len(got) != 2 || got[1] != r.FollowMeLegs[1] {
		t.Fatalf("follow-me legs argument = %v (%v), want %+v", db.args[36], err, r.FollowMeLegs)
	}
	if !strings.Contains(insert, "INTO cdr_legs") || !strings.Contains(insert, "'follow_me'") {
		t.Fatal("the insert does not write cdr_legs with the role follow_me")
	}
}

// TestPeerCallIDStored fails if the callee leg's Call-ID is not the insert
// argument after the origin client: an RTCP-XR report from the callee's phone
// finds the CDR by it.
func TestPeerCallIDStored(t *testing.T) {
	db := &argsDB{}
	w := NewWriter(db, newCounter(), discard)
	now := time.Now()
	if err := w.write(context.Background(), Record{CorrelationID: "p", StartTime: now, EndTime: now, PeerCallID: "leg-b@hello"}); err != nil {
		t.Fatal(err)
	}
	if got := db.args[31]; got != "leg-b@hello" {
		t.Fatalf("peer call id argument = %#v", got)
	}
	if !strings.Contains(insert, "peer_call_id") || !strings.Contains(insert, "$32") {
		t.Fatal("insert does not write peer_call_id")
	}
}

// TestCDROrigin fails if a click-to-call's origin and client are not the
// insert arguments after the codecs, or a record without an origin is not
// stored as a call dialled at a phone.
func TestCDROrigin(t *testing.T) {
	db := &argsDB{}
	w := NewWriter(db, newCounter(), discard)
	now := time.Now()
	r := Record{CorrelationID: "o", StartTime: now, EndTime: now}
	if err := w.write(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if db.args[29] != OriginPhone || db.args[30] != "" {
		t.Fatalf("default origin = %v %v, want phone and no client", db.args[29], db.args[30])
	}
	r.Origin, r.OriginClient = OriginAPI, "token:7"
	if err := w.write(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if db.args[29] != OriginAPI || db.args[30] != "token:7" {
		t.Fatalf("origin = %v %v, want api and token:7", db.args[29], db.args[30])
	}
}

// TestCDRCodecColumns fails if the codec columns are not the two arguments
// after the ring tag and rule (before the origin), are not NULL for a leg
// without a codec, or carry anything but the record's strings.
func TestCDRCodecColumns(t *testing.T) {
	db := &argsDB{}
	w := NewWriter(db, newCounter(), discard)
	now := time.Now()
	r := Record{CorrelationID: "c", StartTime: now, EndTime: now}
	if err := w.write(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(db.args) != 37 || db.args[27] != nil || db.args[28] != nil {
		t.Fatalf("args 28, 29 = %#v, %#v (of %d), want nil, nil of 37", db.args[27], db.args[28], len(db.args))
	}
	r.CallerCodec, r.CalleeCodec = "opus/48000", "PCMU/8000"
	if err := w.write(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if db.args[27] != "opus/48000" || db.args[28] != "PCMU/8000" {
		t.Fatalf("codecs = %#v, %#v", db.args[27], db.args[28])
	}
	if !strings.Contains(insert, "caller_codec, callee_codec, origin, origin_client, peer_call_id, answered_by, srtp)") || !strings.Contains(insert, "$33, $34)") {
		t.Fatal("the insert does not name the codec, origin, peer call id, answered_by and srtp columns with $28 to $34")
	}
}
